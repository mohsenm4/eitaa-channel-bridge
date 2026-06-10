// Package publisher delivers routed messages to a destination (file, HTML site, WordPress).
package publisher

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

// Publisher delivers one routed message. A non-nil error leaves the message un-delivered and retried.
// Returned postID is the target's post identifier (0 for post-less targets or archive follow-ups).
type Publisher interface {
	Name() string
	Publish(ctx context.Context, msg router.Routed) (postID int, err error)
	// Update rewrites an already-published post with the new routed content (Eitaa-side edit sync).
	Update(ctx context.Context, postID int, msg router.Routed) error
	// Delete removes an already-published post (Eitaa-side deletion sync).
	Delete(ctx context.Context, postID int) error
	Close() error
}

func New(cfg config.Target, log *slog.Logger) (Publisher, error) {
	if log == nil {
		log = slog.Default()
	}
	switch cfg.Type {
	case config.TargetFile:
		return NewFile(cfg.File.Path)
	case config.TargetHTML:
		return NewHTML(cfg.HTML, log)
	case config.TargetWordPress:
		return NewWordPress(cfg.WordPress, log), nil
	default:
		return nil, fmt.Errorf("unknown target type %q", cfg.Type)
	}
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
