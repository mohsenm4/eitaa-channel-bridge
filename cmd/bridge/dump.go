package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/jsonio"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/publisher"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/utils"
)

// runDump fetches the channel once and writes raw HTML + routed JSON
// to the storage directory. It does not publish or mutate the seen-set.
func runDump(log *slog.Logger, args []string) {
	fs := flag.NewFlagSet("dump", flag.ExitOnError)
	envPath := fs.String("env", defaultEnvPath, "path to .env file (optional)")
	_ = fs.Parse(args)

	cfg := config.MustLoad(*envPath)
	dataDir := filepath.Dir(cfg.Storage.ArchiveFile)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		utils.Fatal("mkdir %s: %v", dataDir, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := eitaa.New()
	raw, err := client.FetchRaw(ctx, cfg.Source.Channel)
	if err != nil {
		utils.Fatal("fetch: %v", err)
	}
	rawPath := filepath.Join(dataDir, "raw.html")
	if err := os.WriteFile(rawPath, []byte(raw), 0o644); err != nil {
		utils.Fatal("write raw: %v", err)
	}

	msgs, err := eitaa.Parse(cfg.Source.Channel, raw)
	if err != nil {
		utils.Fatal("parse: %v", err)
	}
	rt := router.New(cfg.Source.Channel, cfg.Publishing.Categories, cfg.Publishing.Default)
	routed := rt.RouteAll(msgs)

	prettyPath := filepath.Join(dataDir, "last_dump.json")
	if err := jsonio.WriteIndented(prettyPath, routed); err != nil {
		utils.Fatal("write pretty: %v", err)
	}
	if err := jsonio.WriteJSONL(cfg.Storage.ArchiveFile, msgs); err != nil {
		utils.Fatal("write archive: %v", err)
	}

	type decision struct {
		Routed router.Routed
		OK     bool
		Reason string
	}
	decisions := make([]decision, len(routed))
	published, skipped := 0, 0
	for i, r := range routed {
		ok, reason := publisher.ShouldPublish(cfg.Publishing, r)
		decisions[i] = decision{Routed: r, OK: ok, Reason: reason}
		if ok {
			published++
		} else {
			skipped++
		}
	}

	log.Info("dump complete",
		"channel", cfg.Source.Channel,
		"messages", len(routed),
		"would_publish", published,
		"would_skip", skipped,
		"raw_html", rawPath,
		"pretty", prettyPath,
		"archive", cfg.Storage.ArchiveFile,
	)
	for _, d := range decisions {
		title := d.Routed.Title
		if title == "" {
			title = "(no title)"
		}
		attrs := []any{
			"id", d.Routed.ID,
			"category", d.Routed.CategoryFa,
			"reason", d.Reason,
			"title", utils.DisplayTitle(title, 56),
		}
		if d.OK {
			log.Info("decision publish", attrs...)
		} else {
			log.Info("decision skip", attrs...)
		}
	}
}
