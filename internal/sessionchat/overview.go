package sessionchat

import (
	"context"

	"github.com/zima-oss/jackstay/internal/sessionchat/chatstore"
)

// Overview is a content-free count of a repository's chat activity.
type Overview = chatstore.Overview

// OverviewOf is the chat of the repository whose Git common directory is common: its live sessions
// and the messages waiting for them.
func OverviewOf(ctx context.Context, common string) (Overview, error) {
	channel, err := chatstore.RepositoryChannel(common, "")
	if err != nil {
		return Overview{}, err
	}
	store, err := openStore()
	if err != nil {
		return Overview{}, err
	}
	return store.OverviewOf(ctx, channel.ID)
}
