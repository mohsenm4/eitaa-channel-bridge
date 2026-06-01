package publisher

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

//go:embed htmlsite/*
var htmlAssets embed.FS

// HTML generates a static site under OutputDir. Embedded assets are written
// on first run and never overwritten — delete OutputDir to force a refresh.
// Each Publish rewrites posts.json, which the static page reads.
type HTML struct {
	cfg config.HTMLTarget
	log *slog.Logger

	mu    sync.Mutex
	posts map[int]router.Routed
}

func NewHTML(cfg config.HTMLTarget, log *slog.Logger) (*HTML, error) {
	if log == nil {
		log = slog.Default()
	}
	p := &HTML{cfg: cfg, log: log, posts: map[int]router.Routed{}}
	if err := p.ensureAssets(); err != nil {
		return nil, err
	}
	if err := p.loadExisting(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *HTML) Name() string { return "html:" + p.cfg.OutputDir }

func (p *HTML) Publish(_ context.Context, msg router.Routed) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posts[msg.ID] = msg
	return p.writePostsJSON()
}

func (p *HTML) Close() error { return nil }

func (p *HTML) ensureAssets() error {
	if err := os.MkdirAll(p.cfg.OutputDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", p.cfg.OutputDir, err)
	}
	return fs.WalkDir(htmlAssets, "htmlsite", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := path[len("htmlsite/"):]
		dst := filepath.Join(p.cfg.OutputDir, rel)
		if _, err := os.Stat(dst); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", dst, err)
		}
		data, err := htmlAssets.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
		p.log.Info("html asset written", "path", dst)
		return nil
	})
}

func (p *HTML) loadExisting() error {
	b, err := os.ReadFile(p.postsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read existing posts.json: %w", err)
	}
	var payload sitePayload
	if err := json.Unmarshal(b, &payload); err != nil {
		// Don't crash on a corrupt file — warn loudly so the operator
		// notices their archive is being abandoned.
		p.log.Warn("existing posts.json is unreadable, starting empty",
			"path", p.postsPath(), "err", err)
		return nil
	}
	for _, r := range payload.Posts {
		p.posts[r.ID] = r
	}
	return nil
}

func (p *HTML) writePostsJSON() error {
	payload := sitePayload{
		Site: siteMeta{
			Title:       p.cfg.SiteTitle,
			Subtitle:    "تولید شده توسط Eitaa Channel Bridge",
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Posts: make([]router.Routed, 0, len(p.posts)),
	}
	for _, r := range p.posts {
		payload.Posts = append(payload.Posts, r)
	}
	tmp := p.postsPath() + ".tmp"
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal posts: %w", err)
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write tmp posts.json: %w", err)
	}
	if err := os.Rename(tmp, p.postsPath()); err != nil {
		return fmt.Errorf("rename posts.json: %w", err)
	}
	return nil
}

func (p *HTML) postsPath() string {
	return filepath.Join(p.cfg.OutputDir, "posts.json")
}

type sitePayload struct {
	Site  siteMeta        `json:"site"`
	Posts []router.Routed `json:"posts"`
}

type siteMeta struct {
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle,omitempty"`
	GeneratedAt string `json:"generated_at"`
}
