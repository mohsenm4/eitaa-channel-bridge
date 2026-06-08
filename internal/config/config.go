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
	Target     Target
	Storage    Storage
}

type Source struct {
	Channel      string
	PollInterval time.Duration
	BackfillMax  int
}

type Publishing struct {
	Categories    []Category
	Default       *Category
	SkipHashtags  []string
	InboxHashtags []string
}

type Category struct {
	Hashtag string
	Slug    string
	Label   string
}

// Target describes where messages should be published.
// Only the block matching Type is read.
type Target struct {
	Type      string
	File      FileTarget
	HTML      HTMLTarget
	WordPress WordPressTarget
}

type FileTarget struct {
	Path string
}

type HTMLTarget struct {
	OutputDir string
	SiteTitle string
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

const (
	TargetFile      = "file"
	TargetHTML      = "html"
	TargetWordPress = "wordpress"
)

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

	cfg.Target.Type = envStr("TARGET_TYPE")
	cfg.Target.File.Path = envStr("TARGET_FILE_PATH")
	cfg.Target.HTML.OutputDir = envStr("TARGET_HTML_OUTPUT_DIR")
	cfg.Target.HTML.SiteTitle = envStr("TARGET_HTML_SITE_TITLE")
	cfg.Target.WordPress.URL = envStr("TARGET_WORDPRESS_URL")
	cfg.Target.WordPress.Username = envStr("TARGET_WORDPRESS_USERNAME")
	cfg.Target.WordPress.AppPassword = envStr("TARGET_WORDPRESS_APP_PASSWORD")
	cfg.Target.WordPress.PostType = envStr("TARGET_WORDPRESS_POST_TYPE")
	cfg.Target.WordPress.Status = envStr("TARGET_WORDPRESS_STATUS")

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
		c.Source.PollInterval = 5 * time.Minute
	}
	if c.Storage.SeenFile == "" {
		c.Storage.SeenFile = filepath.Join("data", "seen.json")
	}
	if c.Storage.ArchiveFile == "" {
		c.Storage.ArchiveFile = filepath.Join("data", "messages.jsonl")
	}
	switch c.Target.Type {
	case TargetFile:
		if c.Target.File.Path == "" {
			c.Target.File.Path = filepath.Join("data", "published.jsonl")
		}
	case TargetHTML:
		if c.Target.HTML.OutputDir == "" {
			c.Target.HTML.OutputDir = "site"
		}
		if c.Target.HTML.SiteTitle == "" {
			c.Target.HTML.SiteTitle = "کانال " + c.Source.Channel
		}
	case TargetWordPress:
		if c.Target.WordPress.PostType == "" {
			c.Target.WordPress.PostType = "post"
		}
		if c.Target.WordPress.Status == "" {
			c.Target.WordPress.Status = "draft"
		}
	}
}

func (c *Config) validate() error {
	if c.Source.Channel == "" {
		return errors.New("EITAA_BRIDGE_SOURCE_CHANNEL is required")
	}
	if c.Source.PollInterval < 5*time.Second {
		return fmt.Errorf("EITAA_BRIDGE_SOURCE_POLL_INTERVAL too small (%s): use at least 5s", c.Source.PollInterval)
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
	switch c.Target.Type {
	case TargetFile:
		if c.Target.File.Path == "" {
			return errors.New("EITAA_BRIDGE_TARGET_FILE_PATH is required for target type 'file'")
		}
	case TargetHTML:
		if c.Target.HTML.OutputDir == "" {
			return errors.New("EITAA_BRIDGE_TARGET_HTML_OUTPUT_DIR is required for target type 'html'")
		}
	case TargetWordPress:
		if c.Target.WordPress.URL == "" {
			return errors.New("EITAA_BRIDGE_TARGET_WORDPRESS_URL is required for target type 'wordpress'")
		}
		if _, err := url.Parse(c.Target.WordPress.URL); err != nil {
			return fmt.Errorf("EITAA_BRIDGE_TARGET_WORDPRESS_URL invalid: %w", err)
		}
		if c.Target.WordPress.Username == "" || c.Target.WordPress.AppPassword == "" {
			return errors.New("EITAA_BRIDGE_TARGET_WORDPRESS_USERNAME and _APP_PASSWORD are required")
		}
	case "":
		return errors.New("EITAA_BRIDGE_TARGET_TYPE is required (one of: file, html, wordpress)")
	default:
		return fmt.Errorf("EITAA_BRIDGE_TARGET_TYPE %q is not supported (use: file, html, wordpress)", c.Target.Type)
	}
	return nil
}
