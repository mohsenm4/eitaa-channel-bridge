// Package publisher delivers routed messages to the WordPress target.
package publisher

import (
	"context"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

// Publisher delivers one routed message; non-nil error means retry. Returned postID is the target's post id (0 for archive follow-ups).
type Publisher interface {
	Name() string
	Publish(ctx context.Context, msg router.Routed) (postID int, err error)
	// Update rewrites an already-published post with the new routed content (Eitaa-side edit sync).
	Update(ctx context.Context, postID int, msg router.Routed) error
	// Delete removes an already-published post (Eitaa-side deletion sync).
	Delete(ctx context.Context, postID int) error
	Close() error
}

// ShouldPublish reports whether to publish msg, with a log-friendly reason for either outcome.
func ShouldPublish(p config.Publishing, msg router.Routed) (bool, string) {
	tags := tagSet(msg.Hashtags)
	for _, skip := range p.SkipHashtags {
		if tags[skip] {
			return false, "skip-hashtag #" + skip
		}
	}
	if msg.Category == "" {
		return false, "no matching category"
	}
	return true, "category " + msg.Category
}

func tagSet(tags []string) map[string]bool {
	out := make(map[string]bool, len(tags))
	for _, t := range tags {
		out[t] = true
	}
	return out
}
