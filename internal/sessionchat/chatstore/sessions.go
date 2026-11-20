package chatstore

import (
	"context"
	"errors"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// Bounds of a session record's lists and of the account (sections 6.1 and 6.4).
const (
	maxWatchers      = 32
	maxSubscriptions = 32
	maxLiveSessions  = 256
	maxRelated       = 32
)

// Delivery modes of a session (section 8.1): how messages reach it now.
const (
	DeliveryNative = "native"
	DeliveryHooks  = "hooks"
	DeliveryBridge = "bridge"
	DeliveryPoll   = "poll"
)

// Registration is what a server, a hook or a launcher knows of a session when it registers it.
type Registration struct {
	Key       string // cc:…, cx:…, tk:… or ep:… (section 3.1)
	KeySource string // engine, token or ephemeral
	Engine    string // claude-code, codex or other
	Name      string // "" keeps the session's name, or gives a new one its default
	Channel   Channel
	Worktree  string
	Branch    string
	Parent    string // the key of the session that started this one, "" when none is known
	Delivery  string // native, hooks, bridge or poll
	State     string // the state observed now; "" keeps it (a new session starts unknown)
	Mailbox   bool
}

// Register creates or refreshes a session and its channel (sections 3 and 4.5). A new session is
// subscribed to its own channel; a mailbox to nothing.
func (store *Store) Register(ctx context.Context, registration Registration) (Session, error) {
	if err := checkRegistration(registration); err != nil {
		return Session{}, err
	}
	var result Session
	err := store.update(ctx, func(transaction *transaction) error {
		if err := transaction.ensureChannel(registration.Channel); err != nil {
			return err
		}
		hash := hashKey(registration.Key)
		moment := stamp(transaction.now)
		session, err := transaction.readSession(hash)
		switch {
		case errors.Is(err, os.ErrNotExist):
			session, err = transaction.newSession(registration, hash, moment)
			if err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if registration.Name != "" {
				session.Name = registration.Name
			}
			if session.ChannelID != registration.Channel.ID {
				session.ChannelID = registration.Channel.ID
				if !registration.Mailbox && !slices.Contains(session.Subscriptions, registration.Channel.ID) &&
					len(session.Subscriptions) < maxSubscriptions {
					session.Subscriptions = append(session.Subscriptions, registration.Channel.ID)
				}
			}
			session.KeySource, session.Engine = registration.KeySource, registration.Engine
			session.Worktree, session.Mailbox = registration.Worktree, registration.Mailbox
			if registration.Parent != "" {
				session.Parent = registration.Parent
			}
			if registration.Delivery != "" {
				session.Delivery = registration.Delivery
			}
		}
		if registration.State != "" && registration.State != session.State {
			session.State, session.Since = registration.State, moment
		}
		session.LastSeen = moment
		result = session
		return transaction.writeSession(session)
	})
	return result, err
}

func checkRegistration(registration Registration) error {
	key := registration.Key
	engineKey := strings.HasPrefix(key, "cc:") || strings.HasPrefix(key, "cx:") ||
		strings.HasPrefix(key, "tk:") || strings.HasPrefix(key, "ep:")
	if len(key) > 140 || !engineKey || len(key) < 4 {
		return refuse(ReasonInvalid, "a session key is cc:, cx:, tk: or ep: and an ID, at most 140 characters")
	}
	if registration.Name != "" && !ValidName(registration.Name) {
		return refuse(ReasonInvalid, "a name is 1 to 64 characters on one line, without #, [ or ]")
	}
	if registration.Channel.ID == "" || registration.Channel.Name == "" {
		return refuse(ReasonInvalid, "a session needs its channel")
	}
	switch registration.Engine {
	case EngineClaudeCode, EngineCodex, EngineOther:
	default:
		return refuse(ReasonInvalid, "unknown engine %q", registration.Engine)
	}
	switch registration.KeySource {
	case SourceEngine, SourceToken, SourceEphemeral:
	default:
		return refuse(ReasonInvalid, "unknown key source %q", registration.KeySource)
	}
	switch registration.State {
	case "", StateBusy, StateIdle, StateGone, StateUnknown:
	default:
		return refuse(ReasonInvalid, "unknown state %q", registration.State)
	}
	switch registration.Delivery {
	case "", DeliveryNative, DeliveryHooks, DeliveryBridge, DeliveryPoll:
	default:
		return refuse(ReasonInvalid, "unknown delivery %q", registration.Delivery)
	}
	return nil
}

// newSession is a session's first record, admitted against the live-session count and the store's
// charges (section 7.4).
func (transaction *transaction) newSession(registration Registration, hash, moment string) (Session, error) {
	sessions, err := transaction.listSessions()
	if err != nil {
		return Session{}, err
	}
	live := 0
	hashes := make([]string, 0, len(sessions))
	for _, other := range sessions {
		hashes = append(hashes, hashKey(other.Key))
		if other.State != StateGone {
			live++
		}
	}
	if live >= maxLiveSessions {
		return Session{}, refuse(ReasonCapacity, "the account has %d live chat sessions; wait for one to end", maxLiveSessions)
	}
	if !transaction.admit(deliverystate.SessionCharge, deliverystate.WriteLimit) {
		return Session{}, refuse(ReasonCapacity, "the chat store is full; wait for the sweep to free room")
	}
	transaction.usage.Bytes += deliverystate.SessionCharge
	name := registration.Name
	if name == "" {
		name = DefaultName(registration.Channel, registration.Branch, registration.Worktree)
	}
	state := registration.State
	if state == "" {
		state = StateUnknown
	}
	session := Session{
		V: 1, Key: registration.Key, Ref: shortRef(hash, hashes), Name: name, Engine: registration.Engine,
		KeySource: registration.KeySource, ChannelID: registration.Channel.ID, Mailbox: registration.Mailbox,
		Worktree: registration.Worktree, Parent: registration.Parent, State: state, Since: moment, LastSeen: moment,
		Registered: moment, Delivery: registration.Delivery,
	}
	if session.Delivery == "" {
		session.Delivery = DeliveryPoll
	}
	if !registration.Mailbox {
		session.Subscriptions = []string{registration.Channel.ID}
	}
	return session, nil
}

// Rename gives a session a new name (chat.whoami with `name`).
func (store *Store) Rename(ctx context.Context, key, name string) (Session, error) {
	if !ValidName(name) {
		return Session{}, refuse(ReasonInvalid, "a name is 1 to 64 characters on one line, without #, [ or ]")
	}
	var result Session
	err := store.update(ctx, func(transaction *transaction) error {
		session, err := transaction.sessionOf(key)
		if err != nil {
			return err
		}
		session.Name, session.LastSeen = name, stamp(transaction.now)
		result = session
		return transaction.writeSession(session)
	})
	return result, err
}

// Subscribe adds or removes one channel from a session's subscriptions (section 4.5).
func (store *Store) Subscribe(ctx context.Context, key, channelAddress string, subscribed bool) (Session, error) {
	var result Session
	err := store.update(ctx, func(transaction *transaction) error {
		session, err := transaction.sessionOf(key)
		if err != nil {
			return err
		}
		parsed, err := parseAddress(channelAddress)
		if err != nil || !parsed.channel {
			return refuse(ReasonInvalid, "%q is not a channel address", channelAddress)
		}
		channels, err := transaction.listChannels()
		if err != nil {
			return err
		}
		channel, err := resolveChannel(parsed, channels)
		if err != nil {
			return err
		}
		has := slices.Contains(session.Subscriptions, channel.ID)
		switch {
		case subscribed && !has:
			if len(session.Subscriptions) >= maxSubscriptions {
				return refuse(ReasonCapacity, "a session subscribes to at most %d channels; leave one first", maxSubscriptions)
			}
			session.Subscriptions = append(session.Subscriptions, channel.ID)
		case !subscribed && has:
			session.Subscriptions = slices.DeleteFunc(session.Subscriptions, func(id string) bool { return id == channel.ID })
		}
		session.LastSeen = stamp(transaction.now)
		result = session
		return transaction.writeSession(session)
	})
	return result, err
}

// Sessions lists every session record, gone ones included.
func (store *Store) Sessions(ctx context.Context) ([]Session, error) {
	var result []Session
	err := store.update(ctx, func(transaction *transaction) error {
		sessions, err := transaction.listSessions()
		result = sessions
		return err
	})
	return result, err
}

// Channels lists every known channel.
func (store *Store) Channels(ctx context.Context) ([]Channel, error) {
	var result []Channel
	err := store.update(ctx, func(transaction *transaction) error {
		channels, err := transaction.listChannels()
		result = channels
		return err
	})
	return result, err
}

func (transaction *transaction) readSession(hash string) (Session, error) {
	var session Session
	err := readRecord(transaction.place.session(hash), sessionBound, &session)
	return session, err
}

func (transaction *transaction) writeSession(session Session) error {
	session.V = 1
	return writeRecord(transaction.place.session(hashKey(session.Key)), session, sessionBound)
}

// sessionOf is the registered session of a key, or a refusal that tells it to register.
func (transaction *transaction) sessionOf(key string) (Session, error) {
	session, err := transaction.readSession(hashKey(key))
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, refuse(ReasonInvalid, "this session is not registered with the chat")
	}
	return session, err
}

func (transaction *transaction) listSessions() ([]Session, error) {
	names, err := listNames(transaction.place.sessions())
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(names))
	for _, name := range names {
		var session Session
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		if err := readRecord(transaction.place.session(strings.TrimSuffix(name, ".json")), sessionBound, &session); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(left, right int) bool { return sessions[left].Registered < sessions[right].Registered })
	return sessions, nil
}

// ensureChannel writes a channel's record when it is new, or its name when that changed.
func (transaction *transaction) ensureChannel(channel Channel) error {
	var record channelRecord
	err := readRecord(transaction.place.channel(channel.ID), channelBound, &record)
	switch {
	case err == nil && record.Name == channel.Name:
		return nil
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		if !transaction.admit(deliverystate.ChannelCharge, deliverystate.WriteLimit) {
			return refuse(ReasonCapacity, "the chat store is full; wait for the sweep to free room")
		}
		transaction.usage.Bytes += deliverystate.ChannelCharge
	default:
		return err
	}
	return writeRecord(transaction.place.channel(channel.ID), channelRecord{V: 1, ID: channel.ID, Name: channel.Name}, channelBound)
}

func (transaction *transaction) listChannels() ([]Channel, error) {
	names, err := listNames(transaction.place.channels())
	if err != nil {
		return nil, err
	}
	channels := make([]Channel, 0, len(names))
	for _, name := range names {
		var record channelRecord
		if err := readRecord(transaction.place.channel(strings.TrimSuffix(name, ".json")), channelBound, &record); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		channels = append(channels, Channel{ID: record.ID, Name: record.Name})
	}
	sort.Slice(channels, func(left, right int) bool { return channels[left].ID < channels[right].ID })
	return channels, nil
}

// resolveChannel finds the one channel an address names.
func resolveChannel(parsed address, channels []Channel) (Channel, error) {
	var matches []Channel
	for _, channel := range channels {
		if channel.Name == parsed.name && (parsed.ref == "" || strings.HasPrefix(channel.ID, parsed.ref)) {
			matches = append(matches, channel)
		}
	}
	switch len(matches) {
	case 0:
		return Channel{}, refuse(ReasonUnknownRecipient, "no channel #%s; chat.channels lists them", parsed.name)
	case 1:
		return matches[0], nil
	}
	var addresses []string
	for _, channel := range matches {
		addresses = append(addresses, channelAddressOf(channel, channels))
	}
	return Channel{}, refuse(ReasonAmbiguousRecipient, "several channels are called #%s: %s", parsed.name, strings.Join(addresses, ", "))
}

// sessionAddress is how a listing shows a session: its bare name when one addressable session
// answers to it, `name [ref]` when several do, or when the name is `main` (section 3.2).
func sessionAddress(session Session, sessions []Session) string {
	if session.Name == "main" {
		return fullAddress(session) // bare `main` addresses the parent (section 3.2)
	}
	for _, other := range sessions {
		if other.Key != session.Key && other.Name == session.Name && other.State != StateGone {
			return session.Name + " [" + session.Ref + "]"
		}
	}
	return session.Name
}

// fullAddress is a session's address with its ref, which stays unambiguous when another session
// takes the same name later; stored in envelopes.
func fullAddress(session Session) string { return session.Name + " [" + session.Ref + "]" }

// addressable reports whether a session can be sent to (section 3.2).
func addressable(session Session) bool { return session.State != StateGone }

// target is what an address resolved to: one session, or a channel's subscribers.
type target struct {
	session *Session
	channel *Channel
}

// resolveTarget resolves `to` for the sender (section 3.2).
func resolveTarget(text string, sender Session, sessions []Session, channels []Channel) (target, error) {
	parsed, err := parseAddress(text)
	if err != nil {
		return target{}, refuse(ReasonInvalid, "%s", err.Error())
	}
	if parsed.channel {
		channel, err := resolveChannel(parsed, channels)
		if err != nil {
			return target{}, err
		}
		return target{channel: &channel}, nil
	}
	var matches []Session
	switch {
	case parsed.parent:
		if sender.Parent == "" {
			return target{}, refuse(ReasonNoParent, "this session has no parent session; address it by name")
		}
		for index := range sessions {
			if sessions[index].Key == sender.Parent {
				matches = append(matches, sessions[index])
			}
		}
		if len(matches) == 0 {
			return target{}, refuse(ReasonNoParent, "the session that started this one is not registered with the chat")
		}
	case parsed.ref != "":
		for index := range sessions {
			if sessions[index].Ref == parsed.ref {
				matches = append(matches, sessions[index])
			}
		}
	default:
		for index := range sessions {
			if sessions[index].Name == parsed.name {
				matches = append(matches, sessions[index])
			}
		}
	}
	live := slices.DeleteFunc(slices.Clone(matches), func(session Session) bool { return !addressable(session) })
	switch {
	case len(matches) == 0:
		return target{}, refuse(ReasonUnknownRecipient, "no session %q; chat.sessions lists who can be addressed", text)
	case len(live) == 0:
		return target{}, refuse(ReasonRecipientGone, "%s has ended; the message would never be read", text)
	case len(live) > 1:
		var addresses []string
		for _, session := range live {
			addresses = append(addresses, fullAddress(session))
		}
		return target{}, refuse(ReasonAmbiguousRecipient, "several sessions answer to %q: %s", text, strings.Join(addresses, ", "))
	}
	return target{session: &live[0]}, nil
}

// Scope is what a session's repository allows it to send to (section 2): Restricted is false
// without a `chat.related` key, and Related holds its entries.
type Scope struct {
	Restricted bool
	Related    []string
}

// allowedChannels is the set of channel IDs a restricted sender may send to: its own, and each
// entry that names exactly one known channel (#common included).
func allowedChannels(scope Scope, own string, channels []Channel) map[string]bool {
	allowed := map[string]bool{own: true}
	for _, entry := range scope.Related {
		parsed, err := parseAddress(entry)
		if err != nil || !parsed.channel {
			continue
		}
		if parsed.name == CommonChannel && parsed.ref == "" {
			allowed[CommonChannel] = true
			continue
		}
		if channel, err := resolveChannel(parsed, channels); err == nil {
			allowed[channel.ID] = true
		}
	}
	return allowed
}

// CheckRelated reports what is wrong with each entry of a related list (for jst check): an entry
// that is not a channel address, names no channel yet, or names several.
func CheckRelated(related []string, channels []Channel) []string {
	var problems []string
	if len(related) > maxRelated {
		problems = append(problems, "chat.related holds more than 32 entries")
	}
	for _, entry := range related {
		parsed, err := parseAddress(entry)
		if err != nil || !parsed.channel {
			problems = append(problems, entry+": not a channel address")
			continue
		}
		if parsed.name == CommonChannel && parsed.ref == "" {
			continue
		}
		if _, err := resolveChannel(parsed, channels); err != nil {
			switch ReasonOf(err) {
			case ReasonAmbiguousRecipient:
				problems = append(problems, entry+": names several channels; add its ref, which allows only that one")
			default:
				problems = append(problems, entry+": names no channel yet")
			}
		}
	}
	return problems
}
