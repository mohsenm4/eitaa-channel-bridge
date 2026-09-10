package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const DefaultEnvPath = ".env"

type Config struct {
	Source     Source
	Publishing Publishing
	WordPress  WordPressTarget
	Storage    Storage
}

type Source struct {
	Channel string
	// PollCold is the cold-mode interval — used when no tracked message is recent enough for edits/deletes.
	PollCold time.Duration
	// PollHot is the fast interval used while a tracked message is within EditWindow.
	PollHot time.Duration
	// EditWindow: how long after a message is published to keep polling fast for edits/deletes.
	EditWindow time.Duration
	// SyncWindow: how far back (by message date) published posts stay watched for edits/deletes. The bridge pages
	// through ?before= until the fetched range covers every tracked post inside this window, so a deletion of a post
	// that already scrolled off Eitaa's first page is still mirrored. Zero disables paging (first page only).
	SyncWindow  time.Duration
	BackfillMax int
	// HealthcheckURL: dead-man's-switch ping target (e.g. healthchecks.io); empty disables.
	HealthcheckURL string
	// HealthcheckInterval: must be shorter than the configured dead-man's-switch Period to survive a single missed ping.
	HealthcheckInterval time.Duration
	// HealthcheckFailureThreshold: consecutive publish failures that flip the check to DOWN via /fail. Zero disables.
	HealthcheckFailureThreshold int
}

type Publishing struct {
	Categories    []Category
	Default       *Category
	SkipHashtags  []string
	InboxHashtags []string
	// DedupeWindow: a message with the same text whose timestamp is within this window of an already-published one is treated as an Eitaa double-send and skipped. Zero disables.
	DedupeWindow time.Duration
}

type Category struct {
	Hashtag string
	Slug    string
	Label   string
}

type WordPressTarget struct {
	URL         string
	Username    string
	AppPassword string
	PostType    string
	Status      string
}

type Storage struct {
	SeenFile    string
	ArchiveFile string
}

// Load reads env vars, optionally seeded by a .env file (pass "" to skip); real env vars always win.
func Load(envPath string) (*Config, error) {
	if envPath != "" {
		if err := godotenv.Load(envPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read %s: %w", envPath, err)
		}
	}

	var cfg Config

	cfg.Source.Channel = strings.TrimPrefix(envStr("EITAA_CHANNEL"), "@")
	pc, err := envDuration("POLL_COLD")
	if err != nil {
		return nil, err
	}
	cfg.Source.PollCold = pc
	ph, err := envDuration("POLL_HOT")
	if err != nil {
		return nil, err
	}
	cfg.Source.PollHot = ph
	ew, err := envDuration("EDIT_WINDOW")
	if err != nil {
		return nil, err
	}
	cfg.Source.EditWindow = ew
	sw, err := envDuration("SYNC_WINDOW")
	if err != nil {
		return nil, err
	}
	cfg.Source.SyncWindow = sw
	bm, err := envInt("BACKFILL")
	if err != nil {
		return nil, err
	}
	cfg.Source.BackfillMax = bm
	cfg.Source.HealthcheckURL = envStr("HEALTHCHECK_URL")
	hi, err := envDuration("HEALTHCHECK_INTERVAL")
	if err != nil {
		return nil, err
	}
	cfg.Source.HealthcheckInterval = hi
	hft, err := envInt("HEALTHCHECK_FAIL_THRESHOLD")
	if err != nil {
		return nil, err
	}
	if hft == 0 {
		hft = 3
	}
	cfg.Source.HealthcheckFailureThreshold = hft

	cats, err := parseCategories(envStr("CATEGORIES"))
	if err != nil {
		return nil, err
	}
	cfg.Publishing.Categories = cats
	def, err := parseDefaultCategory(envStr("DEFAULT_CATEGORY"))
	if err != nil {
		return nil, err
	}
	cfg.Publishing.Default = def
	cfg.Publishing.SkipHashtags = parseCommaList(envStr("SKIP_HASHTAGS"))
	cfg.Publishing.InboxHashtags = parseCommaList(envStr("INBOX_HASHTAGS"))
	dw, err := envDuration("DEDUPE_WINDOW")
	if err != nil {
		return nil, err
	}
	cfg.Publishing.DedupeWindow = dw

	cfg.WordPress.URL = envStr("WP_URL")
	cfg.WordPress.Username = envStr("WP_USER")
	cfg.WordPress.AppPassword = envStr("WP_APP_PASSWORD")
	cfg.WordPress.PostType = envStr("WP_POST_TYPE")
	cfg.WordPress.Status = envStr("WP_STATUS")

	cfg.Storage.SeenFile = envStr("SEEN_FILE")
	cfg.Storage.ArchiveFile = envStr("ARCHIVE_FILE")

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// MustLoad is Load with CLI-style exit on error.
func MustLoad(envPath string) *Config {
	if envPath == "" {
		envPath = DefaultEnvPath
	}
	cfg, err := Load(envPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: config: %v\n", err)
		fmt.Fprintln(os.Stderr, "hint: copy .env.example to .env, then fill in your values.")
		os.Exit(1)
	}
	return cfg
}

func envStr(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func envInt(key string) (int, error) {
	s := envStr(key)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envDuration(key string) (time.Duration, error) {
	s := envStr(key)
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func parseCommaList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimPrefix(strings.TrimSpace(p), "#")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseCategories(s string) ([]Category, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]Category, 0, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		fields := strings.Split(p, "|")
		if len(fields) != 3 {
			return nil, fmt.Errorf(
				"CATEGORIES entry %d (%q): expected \"hashtag|slug|label\"", i, p)
		}
		out = append(out, Category{
			Hashtag: strings.TrimPrefix(strings.TrimSpace(fields[0]), "#"),
			Slug:    strings.TrimSpace(fields[1]),
			Label:   strings.TrimSpace(fields[2]),
		})
	}
	return out, nil
}

func parseDefaultCategory(s string) (*Category, error) {
	if s == "" {
		return nil, nil
	}
	fields := strings.Split(s, "|")
	if len(fields) != 2 {
		return nil, fmt.Errorf(
			"DEFAULT_CATEGORY (%q): expected \"slug|label\"", s)
	}
	return &Category{
		Slug:  strings.TrimSpace(fields[0]),
		Label: strings.TrimSpace(fields[1]),
	}, nil
}

func (c *Config) applyDefaults() {
	if c.Source.PollCold == 0 {
		c.Source.PollCold = 6 * time.Hour
	}
	if c.Source.PollHot == 0 {
		c.Source.PollHot = 10 * time.Second
	}
	if c.Source.EditWindow == 0 {
		c.Source.EditWindow = 1 * time.Hour
	}
	if c.Source.SyncWindow == 0 {
		c.Source.SyncWindow = 30 * 24 * time.Hour
	}
	if c.Source.HealthcheckInterval == 0 {
		c.Source.HealthcheckInterval = 12 * time.Minute
	}
	if c.Publishing.DedupeWindow == 0 {
		c.Publishing.DedupeWindow = 10 * time.Second
	}
	if c.Storage.SeenFile == "" {
		c.Storage.SeenFile = filepath.Join("data", "seen.json")
	}
	if c.Storage.ArchiveFile == "" {
		c.Storage.ArchiveFile = filepath.Join("data", "messages.jsonl")
	}
	if c.WordPress.PostType == "" {
		c.WordPress.PostType = "post"
	}
	if c.WordPress.Status == "" {
		c.WordPress.Status = "draft"
	}
}

func (c *Config) validate() error {
	if c.Source.Channel == "" {
		return errors.New("EITAA_CHANNEL is required")
	}
	if c.Source.PollCold < 5*time.Second {
		return fmt.Errorf("POLL_COLD too small (%s): use at least 5s", c.Source.PollCold)
	}
	if c.Source.PollHot < 5*time.Second {
		return fmt.Errorf("POLL_HOT too small (%s): use at least 5s", c.Source.PollHot)
	}
	if c.Source.EditWindow < c.Source.PollHot {
		return fmt.Errorf("EDIT_WINDOW (%s) must be >= POLL_HOT (%s)",
			c.Source.EditWindow, c.Source.PollHot)
	}
	if c.Source.EditWindow < c.Source.PollCold {
		fmt.Fprintf(os.Stderr,
			"warning: EDIT_WINDOW (%s) is shorter than POLL_COLD (%s) — hot mode will rarely activate "+
				"because messages age past the window before the next cold poll sees them.\n",
			c.Source.EditWindow, c.Source.PollCold)
	}
	if c.Source.HealthcheckURL != "" {
		if _, err := url.Parse(c.Source.HealthcheckURL); err != nil {
			return fmt.Errorf("HEALTHCHECK_URL invalid: %w", err)
		}
		if c.Source.HealthcheckInterval < 30*time.Second {
			return fmt.Errorf("HEALTHCHECK_INTERVAL too small (%s): use at least 30s", c.Source.HealthcheckInterval)
		}
	}
	for i, cat := range c.Publishing.Categories {
		if cat.Hashtag == "" || cat.Slug == "" || cat.Label == "" {
			return fmt.Errorf("CATEGORIES[%d]: each entry needs hashtag|slug|label", i)
		}
	}
	if c.Publishing.Default != nil {
		d := c.Publishing.Default
		if d.Slug == "" || d.Label == "" {
			return errors.New("DEFAULT_CATEGORY: slug|label both required")
		}
	}
	if c.WordPress.URL == "" {
		return errors.New("WP_URL is required")
	}
	if _, err := url.Parse(c.WordPress.URL); err != nil {
		return fmt.Errorf("WP_URL invalid: %w", err)
	}
	if c.WordPress.Username == "" || c.WordPress.AppPassword == "" {
		return errors.New("WP_USER and WP_APP_PASSWORD are required")
	}
	return nil
}
