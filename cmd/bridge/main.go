// Command bridge reads messages from an Eitaa channel, classifies them
// according to the posting guide, filters them by the configured
// publishing rules, and delivers the rest to the configured target.
//
// Subcommands:
//
//	bridge dump   — fetch the channel once, parse + route every message
//	                and write JSON to data/. No publishing.
//	bridge run    — poll the channel on source.poll_interval and publish
//	                each new message that matches the publishing rules.
//
// Both commands read config.yaml (override with --config) for source
// channel, publishing rules, and target.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/publisher"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/state"
)

const defaultConfigPath = "config.yaml"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "dump":
		runDump(os.Args[2:])
	case "run":
		runRun(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `bridge — read an Eitaa channel and republish it

usage:
  bridge dump  [--config PATH]
  bridge run   [--config PATH]

dump   fetches the channel once and writes raw HTML + routed JSON
       under data/. Shows which posts WOULD be published. No side effects
       on the target.
run    polls the channel on source.poll_interval. For each new message,
       it classifies, filters by publishing.include_hashtags /
       skip_hashtags, and publishes via the configured target.

flags:
  --config  path to config file (default: config.yaml)

config:
  See config.yaml.example for the schema.
`)
}

func runDump(args []string) {
	fs := flag.NewFlagSet("dump", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath, "path to config file")
	_ = fs.Parse(args)

	cfg := mustLoad(*cfgPath)
	dataDir := filepath.Dir(cfg.Storage.ArchiveFile)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		fatal("mkdir %s: %v", dataDir, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := eitaa.New()
	raw, err := client.FetchRaw(ctx, cfg.Source.Channel)
	if err != nil {
		fatal("fetch: %v", err)
	}
	rawPath := filepath.Join(dataDir, "raw.html")
	if err := os.WriteFile(rawPath, []byte(raw), 0o644); err != nil {
		fatal("write raw: %v", err)
	}

	msgs, err := eitaa.Parse(cfg.Source.Channel, raw)
	if err != nil {
		fatal("parse: %v", err)
	}
	rt := router.New(cfg.Source.Channel, cfg.Publishing.Categories, cfg.Publishing.Default)
	routed := rt.RouteAll(msgs)

	prettyPath := filepath.Join(dataDir, "last_dump.json")
	if err := writeJSONIndented(prettyPath, routed); err != nil {
		fatal("write pretty: %v", err)
	}
	if err := writeJSONL(cfg.Storage.ArchiveFile, msgs); err != nil {
		fatal("write archive: %v", err)
	}

	fmt.Printf("channel:  @%s\n", cfg.Source.Channel)
	fmt.Printf("messages: %d\n", len(routed))
	fmt.Printf("raw html: %s\n", rawPath)
	fmt.Printf("pretty:   %s\n", prettyPath)
	fmt.Printf("archive:  %s\n\n", cfg.Storage.ArchiveFile)

	published, skipped := 0, 0
	for _, r := range routed {
		ok, _ := publisher.ShouldPublish(cfg.Publishing, r)
		if ok {
			published++
		} else {
			skipped++
		}
	}
	fmt.Printf("would publish: %d\n", published)
	fmt.Printf("would skip:    %d\n\n", skipped)

	for _, r := range routed {
		ok, reason := publisher.ShouldPublish(cfg.Publishing, r)
		marker := "✓"
		if !ok {
			marker = "·"
		}
		title := r.Title
		if title == "" {
			title = "(no title)"
		}
		title = truncate(sanitize(title), 56)
		fmt.Printf(" %s #%d  [%-12s] %-26s | %s\n",
			marker, r.ID, r.CategoryFa, "("+reason+")", title)
	}
}

func runRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath, "path to config file")
	_ = fs.Parse(args)

	cfg := mustLoad(*cfgPath)

	store, err := state.Load(cfg.Storage.SeenFile)
	if err != nil {
		fatal("load state: %v", err)
	}
	pub, err := publisher.New(cfg.Target)
	if err != nil {
		fatal("build publisher: %v", err)
	}
	defer pub.Close()

	client := eitaa.New()
	rt := router.New(cfg.Source.Channel, cfg.Publishing.Categories, cfg.Publishing.Default)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("source:   @%s\n", cfg.Source.Channel)
	fmt.Printf("target:   %s\n", pub.Name())
	fmt.Printf("interval: %s\n", cfg.Source.PollInterval)
	if len(cfg.Publishing.Categories) > 0 {
		tags := make([]string, len(cfg.Publishing.Categories))
		for i, c := range cfg.Publishing.Categories {
			tags[i] = c.Hashtag
		}
		fmt.Printf("publish:  #%s\n", join(tags, " #"))
	}
	if cfg.Publishing.Default != nil {
		fmt.Printf("default:  %s (%s)\n", cfg.Publishing.Default.Slug, cfg.Publishing.Default.Label)
	}
	if len(cfg.Publishing.SkipHashtags) > 0 {
		fmt.Printf("skip:     #%s\n", join(cfg.Publishing.SkipHashtags, " #"))
	}
	fmt.Println("press Ctrl+C to stop")

	processBatch := func(msgs []eitaa.Message) (newCount int) {
		for _, m := range msgs {
			if store.Seen(cfg.Source.Channel, m.ID) {
				continue
			}
			r := rt.Route(m)
			ok, reason := publisher.ShouldPublish(cfg.Publishing, r)
			if !ok {
				fmt.Printf("[%s] skipped #%d (%s)\n",
					time.Now().Format("15:04:05"), m.ID, reason)
				store.Mark(cfg.Source.Channel, m.ID)
				newCount++
				continue
			}
			if err := pub.Publish(ctx, r); err != nil {
				fmt.Fprintf(os.Stderr, "[%s] publish #%d: %v\n",
					time.Now().Format(time.RFC3339), m.ID, err)
				continue
			}
			if err := appendJSONL(cfg.Storage.ArchiveFile, m); err != nil {
				fmt.Fprintf(os.Stderr, "archive #%d: %v\n", m.ID, err)
			}
			store.Mark(cfg.Source.Channel, m.ID)
			newCount++
			fmt.Printf("[%s] published #%d [%s] %s\n",
				time.Now().Format("15:04:05"), m.ID, r.CategoryFa, truncate(sanitize(r.Title), 50))
		}
		return newCount
	}

	tick := func() {
		fetchCtx, fcancel := context.WithTimeout(ctx, 30*time.Second)
		defer fcancel()
		msgs, err := client.Fetch(fetchCtx, cfg.Source.Channel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] fetch: %v\n", time.Now().Format(time.RFC3339), err)
			return
		}
		newCount := processBatch(msgs)
		if newCount > 0 {
			if err := store.Save(); err != nil {
				fmt.Fprintf(os.Stderr, "save state: %v\n", err)
			}
		} else {
			fmt.Printf("[%s] no new messages\n", time.Now().Format("15:04:05"))
		}
	}

	// First run: if the seen-set is empty and the user requested
	// backfill, walk Eitaa's ?before= pagination to pull historical
	// posts before entering the polling loop.
	if store.Count(cfg.Source.Channel) == 0 && cfg.Source.BackfillMax > 0 {
		runBackfill(ctx, client, cfg.Source.Channel, cfg.Source.BackfillMax, processBatch)
		if err := store.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "save state after backfill: %v\n", err)
		}
	}

	tick()
	ticker := time.NewTicker(cfg.Source.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nstopping.")
			return
		case <-ticker.C:
			tick()
		}
	}
}

// runBackfill walks Eitaa's ?before= pagination backwards from the
// current page until either max historical messages have been
// collected or the channel has no older posts. Each page is fed to
// processBatch in oldest-first order so the publisher sees the
// historical messages in chronological order.
func runBackfill(ctx context.Context, client *eitaa.Client, channel string, max int,
	processBatch func([]eitaa.Message) int,
) {
	fmt.Printf("[%s] backfill start (max %d)\n", time.Now().Format("15:04:05"), max)

	fetchCtx, fcancel := context.WithTimeout(ctx, 30*time.Second)
	page, err := client.Fetch(fetchCtx, channel)
	fcancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] backfill fetch: %v\n", time.Now().Format(time.RFC3339), err)
		return
	}
	collected := []eitaa.Message{}
	collected = append(collected, page...)

	for len(collected) < max {
		if len(page) == 0 {
			break
		}
		oldest := page[0].ID // Parse returns ascending order
		fetchCtx, fcancel := context.WithTimeout(ctx, 30*time.Second)
		next, err := client.FetchBefore(fetchCtx, channel, oldest)
		fcancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] backfill before=%d: %v\n",
				time.Now().Format(time.RFC3339), oldest, err)
			break
		}
		if len(next) == 0 {
			break
		}
		collected = append(next, collected...)
		page = next
	}
	if len(collected) > max {
		collected = collected[len(collected)-max:]
	}
	count := processBatch(collected)
	fmt.Printf("[%s] backfill done: %d messages, %d processed\n",
		time.Now().Format("15:04:05"), len(collected), count)
}

func mustLoad(path string) *config.Config {
	cfg, err := config.Load(path)
	if err != nil {
		if os.IsNotExist(err) || (path == defaultConfigPath && fileMissing(path)) {
			fatal("config file %s not found — copy config.yaml.example to config.yaml and fill it in", path)
		}
		fatal("config: %v", err)
	}
	return cfg
}

func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

func writeJSONIndented(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func writeJSONL(path string, msgs []eitaa.Message) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			return err
		}
	}
	return nil
}

func appendJSONL(path string, m eitaa.Message) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(m)
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			out = append(out, ' ')
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

func truncate(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}

func join(items []string, sep string) string {
	if len(items) == 0 {
		return ""
	}
	out := items[0]
	for _, s := range items[1:] {
		out += sep + s
	}
	return out
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}
