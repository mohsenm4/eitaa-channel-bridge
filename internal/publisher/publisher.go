// Package publisher defines the Publisher interface and the
// implementations that deliver routed messages to a destination.
//
// All publishers operate on a router.Routed value (the normalized
// message shape) so they have direct access to title, category,
// subtitle, event date, and hashtags — not just raw text.
package publisher

import (
	"context"
	"fmt"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

// Publisher delivers a single routed message to a target.
type Publisher interface {
	// Name returns a short identifier for logs (e.g. "html:site").
	Name() string
	// Publish delivers msg. A non-nil error means the message was NOT
	// delivered and the bridge will retry it on the next tick.
	Publish(ctx context.Context, msg router.Routed) error
	// Close releases any resources held by the publisher.
	Close() error
}

// New builds the Publisher described by cfg.
func New(cfg config.Target) (Publisher, error) {
	switch cfg.Type {
	case config.TargetFile:
		return NewFile(cfg.File.Path)
	case config.TargetHTML:
		return NewHTML(cfg.HTML)
	case config.TargetWordPress:
		return NewWordPress(cfg.WordPress), nil
	default:
		return nil, fmt.Errorf("unknown target type %q", cfg.Type)
	}
}

// ShouldPublish applies the publishing.include_hashtags / skip_hashtags
// rules from config to a routed message. It returns (publish, reason).
// The reason is suitable for human-readable logs.
func ShouldPublish(p config.Publishing, msg router.Routed) (bool, string) {
	tags := tagSet(msg.Hashtags)
	for _, skip := range p.SkipHashtags {
		if tags[skip] {
			return false, "skip-hashtag #" + skip
		}
	}
	if msg.Skip {
		return false, "router skip"
	}
	if len(p.IncludeHashtags) == 0 {
		return true, "all"
	}
	for _, want := range p.IncludeHashtags {
		if tags[want] {
			return true, "#" + want
		}
	}
	return false, "no matching include-hashtag"
}

func tagSet(tags []string) map[string]bool {
	out := make(map[string]bool, len(tags))
	for _, t := range tags {
		out[t] = true
	}
	return out
}
