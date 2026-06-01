package publisher

import (
	"context"
	"errors"
	"log/slog"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

// WordPress is a stub: construction succeeds (so the rest of the pipeline
// boots) but Publish always fails until the REST integration is built.
type WordPress struct {
	cfg config.WordPressTarget
	log *slog.Logger
}

func NewWordPress(cfg config.WordPressTarget, log *slog.Logger) *WordPress {
	if log == nil {
		log = slog.Default()
	}
	log.Warn("wordpress publisher is a stub — publish calls will fail until implemented",
		"url", cfg.URL)
	return &WordPress{cfg: cfg, log: log}
}

func (p *WordPress) Name() string { return "wordpress:" + p.cfg.URL + " (stub)" }

func (p *WordPress) Publish(_ context.Context, _ router.Routed) error {
	return errors.New("wordpress publisher not implemented yet — pending site details")
}

func (p *WordPress) Close() error { return nil }
