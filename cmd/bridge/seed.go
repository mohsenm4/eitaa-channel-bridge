package main

import (
	"context"
	"flag"
	"log/slog"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/utils"
)

// runSeed marks every currently-visible channel message as already-seen so a fresh install only acts on later arrivals.
func runSeed(log *slog.Logger, args []string) {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	envPath := fs.String("env", defaultEnvPath, "path to .env file (optional)")
	_ = fs.Parse(args)

	cfg := config.MustLoad(*envPath)
	r, err := newRunner(cfg, log)
	if err != nil {
		utils.Fatal("init: %v", err)
	}
	defer r.Close()

	channel := cfg.Source.Channel
	already := r.store.Count(channel)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	msgs, err := r.client.Fetch(ctx, channel)
	if err != nil {
		utils.Fatal("fetch: %v", err)
	}

	added := 0
	for _, m := range msgs {
		if !r.store.Seen(channel, m.ID) {
			r.store.Mark(channel, m.ID)
			added++
		}
	}
	if err := r.store.Save(); err != nil {
		utils.Fatal("save: %v", err)
	}

	log.Info("seed done",
		"channel", channel,
		"visible", len(msgs),
		"newly_marked", added,
		"already_tracked", already,
		"total_after", r.store.Count(channel),
	)
}
