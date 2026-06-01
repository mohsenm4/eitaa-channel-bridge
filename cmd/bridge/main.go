// Command bridge reads messages from an Eitaa channel and republishes
// them to a configured target.
//
// Subcommands:
//
//	bridge dump   — fetch the channel once, write raw HTML and parsed
//	                JSON to the storage directory. Useful for inspecting
//	                message structure without publishing.
//	bridge run    — poll the channel on the configured interval and
//	                publish each new message via the configured target.
//
// Both commands read config.yaml (override with --config) for the
// source channel and target site.
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

dump   fetches the channel once and writes raw HTML + parsed JSON
       under data/. No publishing.
run    polls the channel on source.poll_interval and publishes each
       new message via the configured target.

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
	prettyPath := filepath.Join(dataDir, "last_dump.json")
	if err := writeJSONIndented(prettyPath, msgs); err != nil {
		fatal("write pretty: %v", err)
	}
	if err := writeJSONL(cfg.Storage.ArchiveFile, msgs); err != nil {
		fatal("write archive: %v", err)
	}

	fmt.Printf("channel:  @%s\n", cfg.Source.Channel)
	fmt.Printf("messages: %d\n", len(msgs))
	fmt.Printf("raw html: %s\n", rawPath)
	fmt.Printf("pretty:   %s\n", prettyPath)
	fmt.Printf("archive:  %s\n\n", cfg.Storage.ArchiveFile)
	for _, m := range msgs {
		preview := truncate(sanitize(m.Text), 80)
		fmt.Printf("  #%d  %s  views=%d  photos=%d  | %s\n",
			m.ID, m.Date.Format("2006-01-02 15:04"), m.Views, len(m.Photos), preview)
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
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("source:   @%s\n", cfg.Source.Channel)
	fmt.Printf("target:   %s\n", pub.Name())
	fmt.Printf("interval: %s\n", cfg.Source.PollInterval)
	fmt.Println("press Ctrl+C to stop")

	tick := func() {
		fetchCtx, fcancel := context.WithTimeout(ctx, 30*time.Second)
		defer fcancel()
		msgs, err := client.Fetch(fetchCtx, cfg.Source.Channel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] fetch: %v\n", time.Now().Format(time.RFC3339), err)
			return
		}
		newCount := 0
		for _, m := range msgs {
			if store.Seen(cfg.Source.Channel, m.ID) {
				continue
			}
			if err := pub.Publish(ctx, m); err != nil {
				fmt.Fprintf(os.Stderr, "[%s] publish #%d: %v\n", time.Now().Format(time.RFC3339), m.ID, err)
				continue
			}
			if err := appendJSONL(cfg.Storage.ArchiveFile, m); err != nil {
				fmt.Fprintf(os.Stderr, "archive #%d: %v\n", m.ID, err)
			}
			store.Mark(cfg.Source.Channel, m.ID)
			newCount++
			fmt.Printf("[%s] published #%d (%s)\n", time.Now().Format("15:04:05"), m.ID, truncate(sanitize(m.Text), 60))
		}
		if newCount > 0 {
			if err := store.Save(); err != nil {
				fmt.Fprintf(os.Stderr, "save state: %v\n", err)
			}
		} else {
			fmt.Printf("[%s] no new messages\n", time.Now().Format("15:04:05"))
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

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}
