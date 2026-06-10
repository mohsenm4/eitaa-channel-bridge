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

const EnvPrefix = "EITAA_BRIDGE"

const DefaultEnvPath = ".env"

type Config struct {
	Source     Source
	Publishing Publishing
	WordPress  WordPressTarget
	Storage    Storage
}

type Source struct {
	Channel string
	// PollInterval is the cold-mode interval — used when no tracked message is recent enough for edits/deletes.
	PollInterval time.Duration
	// HotPollInterval is the fast interval used while a tracked message is within EditWatchWindow.
	HotPollInterval time.Duration
	// EditWatchWindow: how long after a message is published to keep checking it for edits/deletes.
	EditWatchWindow time.Duration
	BackfillMax     int
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

// Load reads env vars, optionally seeded by a .env file (pass "" to skip).
// A missing .env is fine; real env vars always win over .env values.
func Load(envPath string) (*Config, error) {
	if envPath != "" {
		if err := godotenv.Load(envPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read %s: %w", envPath, err)
		}
	}

	var cfg Config

	cfg.Source.Channel = strings.TrimPrefix(envStr("SOURCE_CHANNEL"), "@")
	pi, err := envDuration("SOURCE_POLL_INTERVAL")
	if err != nil {
		return nil, err
	}
	cfg.Source.PollInterval = pi
	hpi, err := envDuration("SOURCE_HOT_POLL_INTERVAL")
	if err != nil {
		return nil, err
	}
	cfg.Source.HotPollInterval = hpi
	eww, err := envDuration("SOURCE_EDIT_WATCH_WINDOW")
	if err != nil {
		return nil, err
	}
	cfg.Source.EditWatchWindow = eww
	bm, err := envInt("SOURCE_BACKFILL_MAX")
	if err != nil {
		return nil, err
	}
	cfg.Source.BackfillMax = bm

	cats, err := parseCategories(envStr("PUBLISHING_CATEGORIES"))
	if err != nil {
		return nil, err
	}
	cfg.Publishing.Categories = cats
	def, err := parseDefaultCategory(envStr("PUBLISHING_DEFAULT_CATEGORY"))
	if err != nil {
		return nil, err
	}
	cfg.Publishing.Default = def
	cfg.Publishing.SkipHashtags = parseCommaList(envStr("PUBLISHING_SKIP_HASHTAGS"))
	cfg.Publishing.InboxHashtags = parseCommaList(envStr("PUBLISHING_INBOX_HASHTAGS"))
	dw, err := envDuration("PUBLISHING_DEDUPE_WINDOW")
	if err != nil {
		return nil, err
	}
	cfg.Publishing.DedupeWindow = dw

	cfg.WordPress.URL = envStr("TARGET_WORDPRESS_URL")
	cfg.WordPress.Username = envStr("TARGET_WORDPRESS_USERNAME")
	cfg.WordPress.AppPassword = envStr("TARGET_WORDPRESS_APP_PASSWORD")
	cfg.WordPress.PostType = envStr("TARGET_WORDPRESS_POST_TYPE")
	cfg.WordPress.Status = envStr("TARGET_WORDPRESS_STATUS")

	cfg.Storage.SeenFile = envStr("STORAGE_SEEN_FILE")
	cfg.Storage.ArchiveFile = envStr("STORAGE_ARCHIVE_FILE")

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
		fmt.Fprintln(os.Stderr, "hint: copy .env.example to .env, or set EITAA_BRIDGE_* variables directly.")
		os.Exit(1)
	}
	return cfg
}

func envStr(key string) string {
	return strings.TrimSpace(os.Getenv(EnvPrefix + "_" + key))
}

func envInt(key string) (int, error) {
	s := envStr(key)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s_%s: %w", EnvPrefix, key, err)
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
		return 0, fmt.Errorf("%s_%s: %w", EnvPrefix, key, err)
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
				"EITAA_BRIDGE_PUBLISHING_CATEGORIES entry %d (%q): expected \"hashtag|slug|label\"", i, p)
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
			"EITAA_BRIDGE_PUBLISHING_DEFAULT_CATEGORY (%q): expected \"slug|label\"", s)
	}
	return &Category{
		Slug:  strings.TrimSpace(fields[0]),
		Label: strings.TrimSpace(fields[1]),
	}, nil
}

func (c *Config) applyDefaults() {
	if c.Source.PollInterval == 0 {
		c.Source.PollInterval = 6 * time.Hour
	}
	if c.Source.HotPollInterval == 0 {
		c.Source.HotPollInterval = 10 * time.Second
	}
	if c.Source.EditWatchWindow == 0 {
		c.Source.EditWatchWindow = 1 * time.Hour
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
		return errors.New("EITAA_BRIDGE_SOURCE_CHANNEL is required")
	}
	if c.Source.PollInterval < 5*time.Second {
		return fmt.Errorf("EITAA_BRIDGE_SOURCE_POLL_INTERVAL too small (%s): use at least 5s", c.Source.PollInterval)
	}
	if c.Source.HotPollInterval < 5*time.Second {
		return fmt.Errorf("EITAA_BRIDGE_SOURCE_HOT_POLL_INTERVAL too small (%s): use at least 5s", c.Source.HotPollInterval)
	}
	if c.Source.EditWatchWindow < c.Source.HotPollInterval {
		return fmt.Errorf("EITAA_BRIDGE_SOURCE_EDIT_WATCH_WINDOW (%s) must be >= HOT_POLL_INTERVAL (%s)",
			c.Source.EditWatchWindow, c.Source.HotPollInterval)
	}
	for i, cat := range c.Publishing.Categories {
		if cat.Hashtag == "" || cat.Slug == "" || cat.Label == "" {
			return fmt.Errorf("EITAA_BRIDGE_PUBLISHING_CATEGORIES[%d]: each entry needs hashtag|slug|label", i)
		}
	}
	if c.Publishing.Default != nil {
		d := c.Publishing.Default
		if d.Slug == "" || d.Label == "" {
			return errors.New("EITAA_BRIDGE_PUBLISHING_DEFAULT_CATEGORY: slug|label both required")
		}
	}
	if c.WordPress.URL == "" {
		return errors.New("EITAA_BRIDGE_TARGET_WORDPRESS_URL is required")
	}
	if _, err := url.Parse(c.WordPress.URL); err != nil {
		return fmt.Errorf("EITAA_BRIDGE_TARGET_WORDPRESS_URL invalid: %w", err)
	}
	if c.WordPress.Username == "" || c.WordPress.AppPassword == "" {
		return errors.New("EITAA_BRIDGE_TARGET_WORDPRESS_USERNAME and _APP_PASSWORD are required")
	}
	return nil
}
