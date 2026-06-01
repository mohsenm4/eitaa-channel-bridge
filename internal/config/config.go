// Package config loads the bridge's YAML configuration.
//
// The config has three top-level sections:
//
//	source   — which Eitaa channel to read and how often
//	target   — where to publish parsed messages
//	storage  — paths for the seen-set and the raw message archive
//
// A complete annotated example lives in config.yaml.example.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the parsed configuration with defaults applied.
type Config struct {
	Source  Source  `yaml:"source"`
	Target  Target  `yaml:"target"`
	Storage Storage `yaml:"storage"`
}

// Source describes the channel to read from.
type Source struct {
	Channel      string        `yaml:"channel"`
	PollInterval time.Duration `yaml:"poll_interval"`
}

// Target describes where parsed messages should be published.
// Only the block matching Type is read.
type Target struct {
	Type string     `yaml:"type"`
	File FileTarget `yaml:"file,omitempty"`
}

// FileTarget appends each published message to a JSON Lines file.
type FileTarget struct {
	Path string `yaml:"path"`
}

// Storage holds the on-disk state for the bridge.
type Storage struct {
	SeenFile    string `yaml:"seen_file"`
	ArchiveFile string `yaml:"archive_file"`
}

// Known target types.
const (
	TargetFile = "file"
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
	if c.Target.Type == TargetFile && c.Target.File.Path == "" {
		c.Target.File.Path = filepath.Join("data", "published.jsonl")
	}
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
	switch c.Target.Type {
	case TargetFile:
		if c.Target.File.Path == "" {
			return errors.New("target.file.path is required when target.type is file")
		}
	case "":
		return errors.New("target.type is required")
	default:
		return fmt.Errorf("target.type %q is not supported (only %q is implemented so far)", c.Target.Type, TargetFile)
	}
	return nil
}
