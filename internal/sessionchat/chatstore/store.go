// Package chatstore is the agent chat's store (docs/design/chat.md sections 2 to 7): one store per
// local account under ~/.jackstay/.all/chat/v1, small files per record on internal/durable, one
// exclusive lock, a journal for the writes that touch many files, and the delivery states of
// internal/sessionchat/deliverystate, which it imports and never re-derives.
//
// Every operation takes the store's lock and, under the same acquisition, first resolves what the
// journal holds (section 7.3): an operation whose commit record exists is rolled forward, one
// without it is rolled back, and one whose commit record cannot be made durable stops the store,
// so nothing acts beside an operation a later recovery may still roll forward.
package chatstore

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// Lock deadlines (section 7.2): a hook gives up after 2 s and does nothing, a tool after 5 s and
// answers store_busy.
const (
	HookLockWait = 2 * time.Second
	ToolLockWait = 5 * time.Second
)

// Store is the account's chat store.
type Store struct {
	place  layout
	now    func() time.Time
	random io.Reader
	logger func(event, detail string)
	// writeCommitFile and syncFolder are durable.WriteAtomic and durable.SyncFolder for the commit
	// record; a test makes them fail as a disk that cannot make a write durable does.
	writeCommitFile func(path string, data []byte, mode os.FileMode, limit int) error
	syncFolder      func(folder string) error
	// liveLimit, writeLimit and eventLimit are the bounds of sections 6.4 and 7.4; a test lowers them.
	liveLimit              int
	writeLimit, eventLimit int64
}

// Options replace the clock and the source of random bits; a test sets them.
type Options struct {
	Now    func() time.Time
	Random io.Reader
	// Log records what the store dropped or repaired (IDs, states and reason codes, never a body).
	Log func(event, detail string)
}

// Open is the store rooted at folder (the v1 folder of section 7.1).
func Open(folder string, options Options) *Store {
	store := &Store{place: layout{root: folder}, now: options.Now, random: options.Random, logger: options.Log,
		writeCommitFile: durable.WriteAtomic, syncFolder: durable.SyncFolder, liveLimit: maxLiveSessions,
		writeLimit: deliverystate.WriteLimit, eventLimit: deliverystate.EventLimit}
	if store.now == nil {
		store.now = time.Now
	}
	if store.random == nil {
		store.random = rand.Reader
	}
	return store
}

// Default is the account's store, ~/.jackstay/.all/chat/v1.
func Default() (*Store, error) {
	folder, err := homedir.SharedPath("chat", "v1")
	if err != nil {
		return nil, err
	}
	return Open(folder, Options{}), nil
}

func (store *Store) log(event, detail string) {
	if store.logger != nil {
		store.logger(event, detail)
	}
}

// Refusal is a call the store refused, with its reason code (section 5.4) and one line for the
// agent that names what to do next.
type Refusal struct {
	Reason string
	Detail string
}

func (refusal *Refusal) Error() string { return refusal.Reason + ": " + refusal.Detail }

// The refusal reasons of section 5.4.
const (
	ReasonInvalid            = "invalid"
	ReasonUnknownRecipient   = "unknown_recipient"
	ReasonUnknownMessage     = "unknown_message"
	ReasonAmbiguousRecipient = "ambiguous_recipient"
	ReasonNoParent           = "no_parent"
	ReasonOutOfScope         = "out_of_scope"
	ReasonRequestToChannel   = "request_to_channel"
	ReasonIdleUnobservable   = "idle_unobservable"
	ReasonNotRecipient       = "not_recipient"
	ReasonAlreadyAnswered    = "already_answered"
	ReasonRequestClosed      = "request_closed"
	ReasonTooLarge           = "too_large"
	ReasonRateLimited        = "rate_limited"
	ReasonRecipientFull      = "recipient_full"
	ReasonRepeated           = "repeated"
	ReasonLoop               = "loop"
	ReasonCapacity           = "capacity"
	ReasonChatDisabled       = "chat_disabled"
	ReasonStoreBusy          = "store_busy"
)

func refuse(reason, format string, values ...any) *Refusal {
	return &Refusal{Reason: reason, Detail: fmt.Sprintf(format, values...)}
}

// ReasonOf is the refusal reason of err, "" when err is not a refusal.
func ReasonOf(err error) string {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refusal.Reason
	}
	return ""
}

// errStopped is the store stopped by an entry recovery could not resolve (section 7.3).
var errStopped = refuse(ReasonStoreBusy, "the chat store is stopped until a write can be made durable; retry later, and see jst doctor")

// transaction is one operation under the store's lock, after recovery.
type transaction struct {
	store *Store
	place layout
	now   time.Time
	// swept is true once this operation ran the sweep.
	swept bool
}

// update runs change under the store's lock, after recovery.
func (store *Store) update(ctx context.Context, change func(*transaction) error) error {
	if err := makeFolder(store.place.root); err != nil {
		return err
	}
	held, err := durable.LockFile(ctx, store.place.lock())
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return refuse(ReasonStoreBusy, "another chat call holds the store; retry")
		}
		return err
	}
	defer held.Release()
	transaction := &transaction{store: store, place: store.place, now: store.now()}
	resolved, kept, err := transaction.recover()
	if err != nil {
		return err
	}
	if deliverystate.Stopped(kept) {
		return errStopped
	}
	for _, id := range resolved {
		if err := transaction.removeJournal(id); err != nil {
			return err
		}
	}
	// An entry an earlier operation removed must stay removed: until the journal folder is durable
	// nothing runs, so a machine crash cannot bring back an entry a later write has overtaken.
	if exists(transaction.place.journal()) {
		if err := store.syncFolder(transaction.place.journal()); err != nil {
			return errStopped
		}
	}
	if err := transaction.sweepIfDue(); err != nil {
		return err
	}
	return change(transaction)
}

// usage is the charges of the records that exist (section 7.4). It is never stored: every admission
// measures it from the folders, under the lock it already holds, so no crash, failed write or
// removal can leave it counting files that are not there or missing files that are.
type usage struct {
	Bytes    int64
	Messages int
}

// measure counts the charges of every record by listing the folders: usage is a function of which
// files exist (section 7.4).
func (transaction *transaction) measure() (usage, error) {
	place := transaction.place
	var total usage
	charge := func(folder string, each int64) (int, error) {
		names, err := listNames(folder)
		total.Bytes += each * int64(len(names))
		return len(names), err
	}
	nested := func(folder string, each int64) error {
		groups, err := listNames(folder)
		if err != nil {
			return err
		}
		for _, group := range groups {
			if _, err := charge(filepath.Join(folder, group), each); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := charge(place.sessions(), deliverystate.SessionCharge); err != nil {
		return total, err
	}
	if _, err := charge(filepath.Join(place.root, "bindings"), deliverystate.BindingCharge); err != nil {
		return total, err
	}
	if _, err := charge(place.channels(), deliverystate.ChannelCharge); err != nil {
		return total, err
	}
	messages, err := charge(place.messages(), deliverystate.EnvelopeCharge)
	if err != nil {
		return total, err
	}
	total.Messages = messages
	if _, err := charge(place.commits(), deliverystate.CommitCharge); err != nil {
		return total, err
	}
	if _, err := charge(place.idempotency(), deliverystate.IdempotencyCharge); err != nil {
		return total, err
	}
	if err := nested(filepath.Join(place.root, "deliveries"), deliverystate.DeliveryCharge); err != nil {
		return total, err
	}
	if err := nested(filepath.Join(place.root, "inbox"), deliverystate.MarkerCharge); err != nil {
		return total, err
	}
	if err := nested(filepath.Join(place.root, "outbox"), deliverystate.MarkerCharge); err != nil {
		return total, err
	}
	if _, err := charge(place.requests(), deliverystate.MarkerCharge); err != nil {
		return total, err
	}
	bodies, err := listNames(place.bodies())
	if err != nil {
		return total, err
	}
	for _, name := range bodies {
		info, err := os.Lstat(filepath.Join(place.bodies(), name))
		if err == nil {
			total.Bytes += info.Size()
		}
	}
	return total, nil
}

// admit reports whether records of the given charge fit under limit (section 7.4), measured now.
func (transaction *transaction) admit(charge, limit int64) (bool, error) {
	measured, err := transaction.measure()
	if err != nil {
		return false, err
	}
	return deliverystate.Admit(measured.Bytes, charge, limit), nil
}

// listNames lists a folder's entries, without the temporary files of durable writes and the
// journal's own folders; a missing folder is empty.
func listNames(folder string) ([]string, error) {
	entries, err := os.ReadDir(folder)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "broken" {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

// exists reports whether a path exists.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
