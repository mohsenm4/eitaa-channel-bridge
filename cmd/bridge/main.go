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
// channel, publishing rules, and target. Every config value can be
// overridden by an environment variable — see internal/config.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
)

const defaultConfigPath = "config.yaml"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	log := newLogger()
	switch os.Args[1] {
	case "dump":
		runDump(log, os.Args[2:])
	case "run":
		runRun(log, os.Args[2:])
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
       under the storage directory. Shows which posts WOULD be
       published. No side effects on the target.
run    polls the channel on source.poll_interval. For each new
       message, it classifies, filters by publishing rules, and
       publishes via the configured target. On the first run (empty
       seen-set) it walks Eitaa's ?before= pagination up to
       source.backfill_max older messages.

flags:
  --config  path to config file (default: config.yaml)

env:
  EITAA_BRIDGE_<NESTED_KEY>     override config values, e.g.
                                EITAA_BRIDGE_SOURCE_CHANNEL=othername
  EITAA_BRIDGE_LOG_LEVEL        debug|info|warn|error (default: info)

config:
  See config.yaml.example for the schema.
`)
}

// newLogger returns a slog logger that writes to stderr. Level is INFO
// unless EITAA_BRIDGE_LOG_LEVEL is set.
func newLogger() *slog.Logger {
	level := slog.LevelInfo
	if s := os.Getenv("EITAA_BRIDGE_LOG_LEVEL"); s != "" {
		switch strings.ToLower(s) {
		case "debug":
			level = slog.LevelDebug
		case "info":
			level = slog.LevelInfo
		case "warn", "warning":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				// Compact time format for console.
				return slog.String("t", a.Value.Time().Format("15:04:05"))
			}
			return a
		},
	})
	return slog.New(h)
}

func mustLoad(path string) *config.Config {
	cfg, err := config.Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fatal("config file %s not found — copy config.yaml.example to config.yaml and fill it in", path)
		}
		fatal("config: %v", err)
	}
	return cfg
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

// displayTitle returns a single-line, max-N-rune preview of s,
// suitable for inclusion in log lines.
func displayTitle(s string, max int) string {
	return truncate(sanitize(s), max)
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}
