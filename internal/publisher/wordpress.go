package publisher

import (
	"context"
	"errors"
	"log/slog"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

// WordPress publishes routed messages to a WordPress site via the REST API.
//
// This is currently a stub: it accepts configuration so the rest of the
// system can wire up but every Publish returns an error. The
// implementation will be filled in once the target site, post type,
// and media-handling rules are decided (see docs/posting-guide.md and
// README section 6).
type WordPress struct {
	cfg config.WordPressTarget
	log *slog.Logger
}

// NewWordPress returns a WordPress publisher and logs a warning so the
// operator knows publishes will fail. Constructing succeeds so config
// can still be validated and the rest of the pipeline boots normally.
func NewWordPress(cfg config.WordPressTarget, log *slog.Logger) *WordPress {
	if log == nil {
		log = slog.Default()
	}
	log.Warn("wordpress publisher is a stub — publish calls will fail until implemented",
		"url", cfg.URL)
	return &WordPress{cfg: cfg, log: log}
}

// Name implements Publisher.
func (p *WordPress) Name() string { return "wordpress:" + p.cfg.URL + " (stub)" }

// Publish implements Publisher.
func (p *WordPress) Publish(_ context.Context, _ router.Routed) error {
	return errors.New("wordpress publisher not implemented yet — pending site details")
}

// Close implements Publisher.
func (p *WordPress) Close() error { return nil }
