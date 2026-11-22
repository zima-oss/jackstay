package chatstore

import "context"

// Overview is a content-free count of one channel's chat: its live sessions, and the messages
// waiting unread for them (their inbox markers).
type Overview struct {
	Sessions int `json:"sessions"`
	Pending  int `json:"pending"`
}

// OverviewOf counts the channel's sessions that are not gone and the unread markers in their
// inboxes. It only reads, without the lock, recovery or the sweep (jst status writes nothing), so a
// count taken while a step runs may be one off. A marker can outlive its delivery for a moment
// (readers skip and remove it), so Pending is an upper bound.
func (store *Store) OverviewOf(ctx context.Context, channelID string) (Overview, error) {
	var overview Overview
	if err := ctx.Err(); err != nil {
		return overview, err
	}
	reader := &transaction{store: store, place: store.place, now: store.now()}
	err := func(transaction *transaction) error {
		sessions, err := transaction.listSessions()
		if err != nil {
			return err
		}
		for _, session := range sessions {
			if session.ChannelID != channelID || session.State == StateEnded {
				continue
			}
			overview.Sessions++
			names, err := listNames(transaction.place.inbox(hashKey(session.Key)))
			if err != nil {
				return err
			}
			overview.Pending += len(names)
		}
		return nil
	}(reader)
	return overview, err
}
