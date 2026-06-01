// Package config loads the bridge's YAML configuration.
//
// The config has four top-level sections:
//
//	source      — which Eitaa channel to read and how often
//	publishing  — which categories of post to publish, by hashtag
//	target      — where to publish them
//	storage     — paths for the seen-set and the raw message archive
//
// A complete annotated example lives in config.yaml.example.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// DefaultPath is the path Load looks at when called without an argument.
const DefaultPath = "config.yaml"

// EnvPrefix is the prefix for every environment-variable override.
// Example: EITAA_BRIDGE_SOURCE_CHANNEL overrides source.channel.
const EnvPrefix = "EITAA_BRIDGE"

// envBindings lists every config key that may be overridden by env.
// Nested keys use "." here and "_" in the env var name.
var envBindings = []string{
	"source.channel",
	"source.poll_interval",
	"source.backfill_max",
	"target.type",
	"target.html.output_dir",
	"target.html.site_title",
	"target.file.path",
	"target.wordpress.url",
	"target.wordpress.username",
	"target.wordpress.app_password",
	"target.wordpress.post_type",
	"target.wordpress.status",
	"storage.seen_file",
	"storage.archive_file",
}

// Config is the parsed configuration with defaults applied.
type Config struct {
	Source     Source     `yaml:"source"`
	Publishing Publishing `yaml:"publishing"`
	Target     Target     `yaml:"target"`
	Storage    Storage    `yaml:"storage"`
}

// Source describes the channel to read from.
//
// BackfillMax controls how many historical messages to walk back through
// on the FIRST run (when the seen-set for this channel is empty). Eitaa's
// public page only shows the latest ~5–15 posts; setting BackfillMax > 0
// makes the bridge follow the `?before=` pagination on startup until
// BackfillMax messages have been collected or the channel runs out.
// 0 disables backfill.
type Source struct {
	Channel      string        `yaml:"channel"`
	PollInterval time.Duration `yaml:"poll_interval"`
	BackfillMax  int           `yaml:"backfill_max"`
}

// Publishing describes how the bridge classifies and filters posts.
//
// Categories drives both classification (which slug/label a post gets)
// and filtering (a post is only published if it matches at least one
// category — unless Default is set, in which case unmatched posts land
// there). SkipHashtags always wins.
type Publishing struct {
	Categories   []Category `yaml:"categories"`
	Default      *Category  `yaml:"default_category,omitempty"`
	SkipHashtags []string   `yaml:"skip_hashtags"`
}

// Category maps a hashtag to a site category.
type Category struct {
	Hashtag string `yaml:"hashtag"`
	Slug    string `yaml:"slug"`
	Label   string `yaml:"label"`
}

// Target describes where parsed messages should be published.
// Only the block matching Type is read.
type Target struct {
	Type      string          `yaml:"type"`
	File      FileTarget      `yaml:"file,omitempty"`
	HTML      HTMLTarget      `yaml:"html,omitempty"`
	WordPress WordPressTarget `yaml:"wordpress,omitempty"`
}

// FileTarget appends each published message to a JSON Lines file.
type FileTarget struct {
	Path string `yaml:"path"`
}

// HTMLTarget generates a static HTML site under OutputDir.
type HTMLTarget struct {
	OutputDir string `yaml:"output_dir"`
	SiteTitle string `yaml:"site_title,omitempty"`
}

// WordPressTarget posts each message to a WordPress site (not yet implemented).
type WordPressTarget struct {
	URL         string `yaml:"url"`
	Username    string `yaml:"username"`
	AppPassword string `yaml:"app_password"`
	PostType    string `yaml:"post_type,omitempty"`
	Status      string `yaml:"status,omitempty"`
}

// Storage holds the on-disk state for the bridge.
type Storage struct {
	SeenFile    string `yaml:"seen_file"`
	ArchiveFile string `yaml:"archive_file"`
}

// Known target types.
const (
	TargetFile      = "file"
	TargetHTML      = "html"
	TargetWordPress = "wordpress"
)

// Load reads YAML from path, applies environment overrides, and returns
// a validated config.
//
// Env vars under EITAA_BRIDGE_ override file values, with "_" between
// nested keys — e.g. EITAA_BRIDGE_SOURCE_CHANNEL=othername replaces
// source.channel, EITAA_BRIDGE_TARGET_WORDPRESS_APP_PASSWORD overrides
// the WordPress credential.
//
// A missing file is reported with os.ErrNotExist via the wrapped error,
// so callers can distinguish it.
func Load(path string) (*Config, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	for _, key := range envBindings {
		_ = v.BindEnv(key)
	}
	var cfg Config
	if err := v.Unmarshal(&cfg, useYAMLTags); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// MustLoad is Load with the CLI-style "exit on error" behaviour: a
// missing config file prints a hint about config.yaml.example and
// any other error is printed verbatim. Intended for short-lived
// commands; library callers should use Load and handle the error.
func MustLoad(path string) *Config {
	cfg, err := Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr,
				"error: config file %s not found — copy config.yaml.example to config.yaml and fill it in\n",
				path)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "error: config: %v\n", err)
		os.Exit(1)
	}
	return cfg
}

// useYAMLTags tells mapstructure to read the same struct tags that
// describe the YAML file, so we avoid duplicating each tag.
// It also enables the standard hooks for time.Duration and string slices.
func useYAMLTags(dc *mapstructure.DecoderConfig) {
	dc.TagName = "yaml"
	dc.DecodeHook = mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
	)
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
	// Strip leading # from hashtag entries so users can write either form.
	c.Publishing.SkipHashtags = stripHashes(c.Publishing.SkipHashtags)
	for i := range c.Publishing.Categories {
		c.Publishing.Categories[i].Hashtag = stripHash(c.Publishing.Categories[i].Hashtag)
	}
}

func stripHashes(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, stripHash(s))
	}
	return out
}

func stripHash(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "#")
}

func (c *Config) validate() error {
	if c.Source.Channel == "" {
		return errors.New("source.channel is required")
	}
	if strings.HasPrefix(c.Source.Channel, "@") {
		return errors.New("source.channel must not include the leading @")
	}
	if c.Source.PollInterval < 5*time.Second {
		return fmt.Errorf("source.poll_interval too small (%s): use at least 5s", c.Source.PollInterval)
	}
	for i, cat := range c.Publishing.Categories {
		if cat.Hashtag == "" || cat.Slug == "" || cat.Label == "" {
			return fmt.Errorf("publishing.categories[%d]: hashtag, slug and label are all required", i)
		}
	}
	if c.Publishing.Default != nil {
		d := c.Publishing.Default
		if d.Slug == "" || d.Label == "" {
			return errors.New("publishing.default_category: slug and label are required")
		}
	}
	switch c.Target.Type {
	case TargetFile:
		if c.Target.File.Path == "" {
			return errors.New("target.file.path is required when target.type is file")
		}
	case TargetHTML:
		if c.Target.HTML.OutputDir == "" {
			return errors.New("target.html.output_dir is required when target.type is html")
		}
	case TargetWordPress:
		if c.Target.WordPress.URL == "" {
			return errors.New("target.wordpress.url is required when target.type is wordpress")
		}
		if _, err := url.Parse(c.Target.WordPress.URL); err != nil {
			return fmt.Errorf("target.wordpress.url invalid: %w", err)
		}
		if c.Target.WordPress.Username == "" || c.Target.WordPress.AppPassword == "" {
			return errors.New("target.wordpress.username and target.wordpress.app_password are required")
		}
	case "":
		return errors.New("target.type is required (one of: file, html, wordpress)")
	default:
		return fmt.Errorf("target.type %q is not supported (use: file, html, wordpress)", c.Target.Type)
	}
	return nil
}
