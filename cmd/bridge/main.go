// Command bridge fetches messages from a public Eitaa channel and writes
// them to local files for inspection or downstream publishing.
//
// Subcommands:
//
//	bridge dump   — fetch once, write raw HTML and parsed JSON. Useful for
//	                inspecting the structure of the messages.
//	bridge poll   — fetch on an interval, append only new messages to the
//	                archive, and update the seen-set.
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

	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/state"
)

const (
	defaultChannel  = "Merajyan"
	defaultDataDir  = "data"
	defaultInterval = 5 * time.Minute
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "dump":
		runDump(args)
	case "poll":
		runPoll(args)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `bridge — Eitaa channel reader

usage:
  bridge dump  [--channel NAME] [--data-dir DIR]
  bridge poll  [--channel NAME] [--data-dir DIR] [--interval DURATION]

dump   fetches the channel once and writes raw HTML + parsed JSON to disk.
poll   loops on --interval and appends only new messages to the archive.

flags:
  --channel    Eitaa channel username without the @ (default: Merajyan)
  --data-dir   directory for output files (default: data)
  --interval   poll interval, e.g. 30s, 5m (default: 5m)
`)
}

func runDump(args []string) {
	fs := flag.NewFlagSet("dump", flag.ExitOnError)
	channel := fs.String("channel", defaultChannel, "Eitaa channel name (no @)")
	dataDir := fs.String("data-dir", defaultDataDir, "directory for output files")
	_ = fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := eitaa.New()
	raw, err := client.FetchRaw(ctx, *channel)
	if err != nil {
		fatal("fetch: %v", err)
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		fatal("mkdir: %v", err)
	}
	rawPath := filepath.Join(*dataDir, "raw.html")
	if err := os.WriteFile(rawPath, []byte(raw), 0o644); err != nil {
		fatal("write raw: %v", err)
	}

	msgs, err := eitaa.Parse(*channel, raw)
	if err != nil {
		fatal("parse: %v", err)
	}

	dumpPath := filepath.Join(*dataDir, "last_dump.json")
	if err := writeJSONIndented(dumpPath, msgs); err != nil {
		fatal("write dump: %v", err)
	}
	jsonlPath := filepath.Join(*dataDir, "messages.jsonl")
	if err := writeJSONL(jsonlPath, msgs); err != nil {
		fatal("write jsonl: %v", err)
	}

	fmt.Printf("channel:   @%s\n", *channel)
	fmt.Printf("messages:  %d\n", len(msgs))
	fmt.Printf("raw html:  %s\n", rawPath)
	fmt.Printf("pretty:    %s\n", dumpPath)
	fmt.Printf("jsonl:     %s\n", jsonlPath)
	fmt.Println()
	for _, m := range msgs {
		preview := truncateRunes(sanitizePreview(m.Text), 80)
		fmt.Printf("  #%d  %s  views=%d  photos=%d  | %s\n",
			m.ID, m.Date.Format("2006-01-02 15:04"), m.Views, len(m.Photos), preview)
	}
}

func runPoll(args []string) {
	fs := flag.NewFlagSet("poll", flag.ExitOnError)
	channel := fs.String("channel", defaultChannel, "Eitaa channel name (no @)")
	dataDir := fs.String("data-dir", defaultDataDir, "directory for output files")
	interval := fs.Duration("interval", defaultInterval, "poll interval (e.g. 30s, 5m)")
	_ = fs.Parse(args)

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		fatal("mkdir: %v", err)
	}
	statePath := filepath.Join(*dataDir, "seen.json")
	store, err := state.Load(statePath)
	if err != nil {
		fatal("load state: %v", err)
	}
	jsonlPath := filepath.Join(*dataDir, "messages.jsonl")
	client := eitaa.New()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("polling @%s every %s — press Ctrl+C to stop\n", *channel, *interval)
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	tick := func() {
		fetchCtx, fcancel := context.WithTimeout(ctx, 30*time.Second)
		defer fcancel()
		msgs, err := client.Fetch(fetchCtx, *channel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] fetch error: %v\n", time.Now().Format(time.RFC3339), err)
			return
		}
		newCount := 0
		for _, m := range msgs {
			if store.Seen(*channel, m.ID) {
				continue
			}
			if err := appendJSONL(jsonlPath, m); err != nil {
				fmt.Fprintf(os.Stderr, "append error: %v\n", err)
				continue
			}
			store.Mark(*channel, m.ID)
			newCount++
			preview := truncateRunes(sanitizePreview(m.Text), 60)
			fmt.Printf("[%s] new #%d photos=%d | %s\n",
				time.Now().Format("15:04:05"), m.ID, len(m.Photos), preview)
		}
		if newCount > 0 {
			if err := store.Save(); err != nil {
				fmt.Fprintf(os.Stderr, "save state: %v\n", err)
			}
		} else {
			fmt.Printf("[%s] no new messages (checked %d)\n",
				time.Now().Format("15:04:05"), len(msgs))
		}
	}

	tick()
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

func writeJSONIndented(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func writeJSONL(path string, msgs []eitaa.Message) error {
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
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(m)
}

func truncateRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}

func sanitizePreview(s string) string {
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

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}
