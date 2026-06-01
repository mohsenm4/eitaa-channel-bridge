package publisher

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

//go:embed htmlsite/*
var htmlAssets embed.FS

// HTML generates a static site under OutputDir.
//
// On first Publish (or open) the embedded index.html, style.css, and app.js
// are copied into OutputDir. Every Publish then updates posts.json, which
// the static page reads to render the list. The site is fully usable by
// pointing any static file server at OutputDir.
type HTML struct {
	cfg config.HTMLTarget

	mu    sync.Mutex
	posts map[int]router.Routed
}

// NewHTML returns an HTML publisher. The output directory and the
// embedded static assets are created on the first Publish.
func NewHTML(cfg config.HTMLTarget) (*HTML, error) {
	p := &HTML{cfg: cfg, posts: map[int]router.Routed{}}
	if err := p.ensureAssets(); err != nil {
		return nil, err
	}
	if err := p.loadExisting(); err != nil {
		return nil, err
	}
	return p, nil
}

// Name implements Publisher.
func (p *HTML) Name() string { return "html:" + p.cfg.OutputDir }

// Publish implements Publisher.
func (p *HTML) Publish(_ context.Context, msg router.Routed) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posts[msg.ID] = msg
	return p.writePostsJSON()
}

// Close implements Publisher.
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
		// Strip the "htmlsite/" prefix so files land at the root of OutputDir.
		rel := path[len("htmlsite/"):]
		dst := filepath.Join(p.cfg.OutputDir, rel)
		data, err := htmlAssets.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}
		return os.WriteFile(dst, data, 0o644)
	})
}

// loadExisting restores already-published posts from disk so that
// stopping and restarting the bridge does not wipe the site.
func (p *HTML) loadExisting() error {
	b, err := os.ReadFile(p.postsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read existing posts.json: %w", err)
	}
	var payload sitePayload
	if err := json.Unmarshal(b, &payload); err != nil {
		// A corrupt file should not crash the publisher — start clean.
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
