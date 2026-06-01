package publisher

import (
	"context"
	"errors"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

// WordPress publishes routed messages to a WordPress site via the REST API.
//
// This is currently a stub: it validates configuration but Publish returns
// an error. The implementation will be filled in once the target site,
// post type, and media-handling rules are decided
// (see docs/posting-guide.md and README section 6).
type WordPress struct {
	cfg config.WordPressTarget
}

// NewWordPress returns a WordPress publisher.
func NewWordPress(cfg config.WordPressTarget) *WordPress {
	return &WordPress{cfg: cfg}
}

// Name implements Publisher.
func (p *WordPress) Name() string { return "wordpress:" + p.cfg.URL }

// Publish implements Publisher.
func (p *WordPress) Publish(_ context.Context, _ router.Routed) error {
	return errors.New("wordpress publisher not implemented yet — pending site details")
}

// Close implements Publisher.
func (p *WordPress) Close() error { return nil }
