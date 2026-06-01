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

	"gopkg.in/yaml.v3"
)

// Config is the parsed configuration with defaults applied.
type Config struct {
	Source     Source     `yaml:"source"`
	Publishing Publishing `yaml:"publishing"`
	Target     Target     `yaml:"target"`
	Storage    Storage    `yaml:"storage"`
}

// Source describes the channel to read from.
type Source struct {
	Channel      string        `yaml:"channel"`
	PollInterval time.Duration `yaml:"poll_interval"`
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

// Load reads, parses, validates, and defaults the YAML config at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
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
