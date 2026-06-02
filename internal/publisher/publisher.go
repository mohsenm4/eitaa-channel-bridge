// Package publisher delivers routed messages to a destination (file, HTML site, WordPress).
package publisher

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

// Publisher delivers a single routed message. A non-nil Publish error
// keeps the message un-delivered and triggers a retry on the next tick.
type Publisher interface {
	Name() string
	Publish(ctx context.Context, msg router.Routed) error
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

// ShouldPublish returns (publish?, log-friendly reason). A message is
// published if it carries a recognised category — OR if it's an
// #آرشیو follow-up, which has its own attach-to-previous flow in the
// WordPress publisher.
func ShouldPublish(p config.Publishing, msg router.Routed) (bool, string) {
	tags := tagSet(msg.Hashtags)
	for _, skip := range p.SkipHashtags {
		if tags[skip] {
			return false, "skip-hashtag #" + skip
		}
	}
	if msg.IsArchive() {
		return true, "archive — will attach to last post"
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
