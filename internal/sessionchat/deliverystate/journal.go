package deliverystate

// The journalled writes of a send or a response (section 7.3): what recovery does with an entry,
// when a message may be served, and how a write is admitted against the store's charges (section 7.4).

// Commit is what is known of an operation's commit record.
type Commit string

// The commit record's states.
const (
	// NoCommit: the record does not exist.
	NoCommit Commit = "none"
	// CommitWritten: the record was renamed into place but its folder could not be fsynced
	// (durable.ErrNotSynced), so a crash of the machine may still undo it.
	CommitWritten Commit = "written"
	// CommitDurable: the record and its folder are fsynced.
	CommitDurable Commit = "durable"
)

// Recovery is what recovery does with a journal entry.
type Recovery string

// The recovery decisions.
const (
	// RollBack removes the operation's files: those of its message ID and the idempotency entry the
	// journal entry names. Nothing of it was served.
	RollBack Recovery = "roll-back"
	// RollForward applies what follows from the commit (step 3), writes the derived files (step 4)
	// and removes the entry.
	RollForward Recovery = "roll-forward"
	// KeepEntry leaves the entry: the commit record exists but its folder still cannot be fsynced,
	// so the operation is neither undone nor served, and the store is Stopped until a recovery
	// resolves it.
	KeepEntry Recovery = "keep"
)

// Recover decides an entry. Synced reports whether fsyncing the commit record's folder, which
// recovery tries before it rolls a written record forward, succeeded.
func Recover(commit Commit, synced bool) Recovery {
	switch {
	case commit == NoCommit:
		return RollBack
	case commit == CommitDurable || synced:
		return RollForward
	}
	return KeepEntry
}

// Served reports whether any reader, hook or pusher may act on a message: its commit record is
// durable and no journal entry for it is left. A message whose entry is kept does not exist yet
// for anyone but recovery, so nothing of it is pushed before a crash can no longer undo it.
func Served(commit Commit, journalled bool) bool {
	return commit == CommitDurable && !journalled
}

// Charges of the records a send creates (section 7.4), in bytes. Each is charged its bound when
// created, whatever its size on disk.
const (
	EnvelopeCharge    = 48 << 10
	DeliveryCharge    = 4 << 10
	CommitCharge      = 1 << 10
	MarkerCharge      = 512
	IdempotencyCharge = 512
	SessionCharge     = 64 << 10
	BindingCharge     = 8 << 10
	ChannelCharge     = 16 << 10
	// StoreBytes is the store's whole bound; WriteLimit keeps its last MiB for journal entries and
	// temporary files, and binds every path that creates a record.
	StoreBytes = 64 << 20
	WriteLimit = StoreBytes - 1<<20
	// EventLimit: events are stored only below 90% of the bound.
	EventLimit = StoreBytes / 10 * 9
)

// SendCharge is the charge of a send with the given body and number of deliveries: every record it
// creates, the derived ones of step 4 (an inbox marker per pending delivery, the outbox marker and
// the idempotency entry) included, so that a committed send never needs room it was not admitted
// with, and recovery never refuses to roll one forward.
func SendCharge(bodyBytes, deliveries int, idempotent bool) int64 {
	charge := int64(bodyBytes) + EnvelopeCharge + CommitCharge +
		int64(deliveries)*(DeliveryCharge+MarkerCharge) + MarkerCharge
	if idempotent {
		charge += IdempotencyCharge
	}
	return charge
}

// Admit reports whether a write that creates records of the given charge fits under the limit.
func Admit(used, charge, limit int64) bool {
	return charge >= 0 && used <= limit-charge
}

// RetryAnswer is what a send with an idempotency key already used gets.
type RetryAnswer string

// The answers to a retry.
const (
	// FreshSend: no message is known by the key; the send is new.
	FreshSend RetryAnswer = "fresh"
	// Duplicate: the idempotency entry names a message whose commit record exists; its IDs are
	// returned.
	Duplicate RetryAnswer = "duplicate"
	// StaleEntry: the entry names a message that is gone (removed by retention, or a removal cut
	// short between the commit record and the entry); the entry is removed and the send is fresh.
	// A key is remembered as long as its message is kept, no longer.
	StaleEntry RetryAnswer = "stale"
)

// ResolveRetry answers a send by its key. It runs after recovery, so no journal entry is left.
func ResolveRetry(idempotencyEntry, committed bool) RetryAnswer {
	switch {
	case idempotencyEntry && committed:
		return Duplicate
	case idempotencyEntry:
		return StaleEntry
	}
	return FreshSend
}

// ResponseAdmitted decides whether a response may begin: its request must be open.
func ResponseAdmitted(request RequestState) error {
	switch {
	case request == Answered:
		return ErrAlreadyAnswered
	case request != Open:
		return ErrRequestClosed
	}
	return nil
}

// Stopped reports whether the store is stopped: recovery left an entry it could not resolve (a
// commit record whose folder cannot be fsynced). Then no operation runs, reader or writer, hook,
// pusher, sweep or freeze: a tool answers store_busy and a hook does nothing, until a recovery
// resolves the entry. Nothing can then act beside an operation a later recovery may still roll
// forward (section 7.3).
func Stopped(keptEntries int) bool { return keptEntries > 0 }

// FreezeMayProceed reports whether a review mailbox may freeze: none of the deliveries of its open
// requests is `sending`, so no write of a question is under way when the freeze closes it (section
// 9.1). A pass that stalled past 60 s has been swept to `delivered` (unconfirmed) and no longer
// holds the freeze.
func FreezeMayProceed(questionDeliveries []Delivery) bool {
	for _, delivery := range questionDeliveries {
		if delivery.State == Sending {
			return false
		}
	}
	return true
}
