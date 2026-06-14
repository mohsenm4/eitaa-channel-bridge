package main

import (
	"context"
	"flag"
	"log/slog"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/utils"
)

// runSeed marks every message currently visible on the channel page as already-seen,
// without publishing anything. Use this on a fresh install (or after deleting seen.json)
// when you want the bridge to ignore everything posted before now and only act on
// messages that arrive from this point forward.
//
// BACKFILL=0 alone does NOT achieve this — it only disables ?before= pagination.
// The first tick still fetches the visible page (~20 latest messages) and treats
// them as new because seen.json is empty.
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
