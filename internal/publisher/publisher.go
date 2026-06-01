// Package publisher defines the Publisher interface and provides
// implementations that deliver parsed Eitaa messages to different targets.
//
// Today only the File publisher is implemented. When the target site
// (WordPress, etc.) is decided, add a new file in this package and
// extend the New factory below.
package publisher

import (
	"context"
	"fmt"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
)

// Publisher delivers a single message to a target.
// Implementations may be called concurrently and must guard their own state.
type Publisher interface {
	// Name returns a short identifier for logs (e.g. "file:data/published.jsonl").
	Name() string
	// Publish delivers msg. A non-nil error means the message was NOT delivered
	// and the bridge will retry it on the next tick.
	Publish(ctx context.Context, msg eitaa.Message) error
	// Close releases any resources held by the publisher.
	Close() error
}

// New builds the Publisher described by cfg.
func New(cfg config.Target) (Publisher, error) {
	switch cfg.Type {
	case config.TargetFile:
		return NewFile(cfg.File.Path)
	default:
		return nil, fmt.Errorf("unknown target type %q", cfg.Type)
	}
}
