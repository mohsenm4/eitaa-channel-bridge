package publisher

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

// WordPress publishes a routed message as a WP post (status from cfg,
// typically "draft") via the REST API and Basic auth using an
// Application Password.
type WordPress struct {
	cfg  config.WordPressTarget
	log  *slog.Logger
	http *http.Client

	// Lazy-loaded: WP category Name -> ID. Filled on first Publish.
	catByName map[string]int
}

func NewWordPress(cfg config.WordPressTarget, log *slog.Logger) *WordPress {
	if log == nil {
		log = slog.Default()
	}
	// TODO: fatemyoon.ir SSL cert is currently expired. Drop
	// InsecureSkipVerify once the host has renewed.
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	log.Warn("wordpress: TLS verification disabled (site cert expired)",
		"url", cfg.URL)
	return &WordPress{
		cfg:  cfg,
		log:  log,
		http: &http.Client{Timeout: 30 * time.Second, Transport: tr},
	}
}

func (p *WordPress) Name() string { return "wordpress:" + p.cfg.URL }

func (p *WordPress) Close() error { return nil }

func (p *WordPress) Publish(ctx context.Context, msg router.Routed) error {
	if msg.CategoryFa == "" {
		return fmt.Errorf("routed message has no category label")
	}
	if err := p.loadCategories(ctx); err != nil {
		return fmt.Errorf("load WP categories: %w", err)
	}
	catID, ok := p.catByName[msg.CategoryFa]
	if !ok {
		return fmt.Errorf("WP category %q not found on the site — create it or fix the .env label", msg.CategoryFa)
	}

	payload := map[string]any{
		"title":      msg.Title,
		"content":    p.renderHTML(msg),
		"status":     p.cfg.Status,
		"categories": []int{catID},
	}
	body, _ := json.Marshal(payload)

	resp, err := p.do(ctx, http.MethodPost, "/wp-json/wp/v2/posts", body)
	if err != nil {
		return err
	}

	var out struct {
		ID   int    `json:"id"`
		Link string `json:"link"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return fmt.Errorf("decode WP post response: %w (body: %s)", err, snippet(resp))
	}

	p.log.Info("wordpress: published",
		"eitaa_id", msg.ID,
		"wp_id", out.ID,
		"status", p.cfg.Status,
		"category", msg.CategoryFa,
		"title", msg.Title,
		"link", out.Link,
	)
	return nil
}

func (p *WordPress) loadCategories(ctx context.Context) error {
	if p.catByName != nil {
		return nil
	}
	cats, err := p.fetchAllCategories(ctx)
	if err != nil {
		return err
	}
	m := make(map[string]int, len(cats))
	for _, c := range cats {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		if existing, dup := m[name]; dup {
			p.log.Warn("wordpress: duplicate category name on site",
				"name", name, "first_id", existing, "second_id", c.ID)
			continue
		}
		m[name] = c.ID
	}
	p.catByName = m
	p.log.Info("wordpress: loaded categories", "count", len(m))
	return nil
}

type wpCategory struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func (p *WordPress) fetchAllCategories(ctx context.Context) ([]wpCategory, error) {
	const perPage = 100
	var all []wpCategory
	for page := 1; ; page++ {
		path := fmt.Sprintf("/wp-json/wp/v2/categories?per_page=%d&page=%d", perPage, page)
		body, err := p.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var batch []wpCategory
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("decode categories page %d: %w", page, err)
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if len(batch) < perPage {
			break
		}
	}
	return all, nil
}

func (p *WordPress) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	url := strings.TrimRight(p.cfg.URL, "/") + path
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.SetBasicAuth(p.cfg.Username, p.cfg.AppPassword)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("WP %s %s: HTTP %d: %s",
			method, path, resp.StatusCode, snippet(respBody))
	}
	return respBody, nil
}

func (p *WordPress) renderHTML(msg router.Routed) string {
	var head strings.Builder
	if msg.Subtitle != "" {
		head.WriteString("<p><strong>")
		head.WriteString(htmlEscape(msg.Subtitle))
		head.WriteString("</strong></p>\n")
	}
	if msg.EventDate != "" {
		head.WriteString("<p>📅 ")
		head.WriteString(htmlEscape(msg.EventDate))
		head.WriteString("</p>\n")
	}

	var body strings.Builder
	titleTrim := strings.TrimSpace(msg.Title)
	for _, raw := range strings.Split(msg.Text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line == "." {
			continue
		}
		if line == titleTrim {
			continue
		}
		if isHashtagOnlyLine(line) {
			continue
		}
		body.WriteString("<p>")
		body.WriteString(htmlEscape(line))
		body.WriteString("</p>\n")
	}
	return head.String() + body.String()
}

func isHashtagOnlyLine(line string) bool {
	if line == "" || !strings.HasPrefix(line, "#") {
		return false
	}
	for _, f := range strings.Fields(line) {
		if !strings.HasPrefix(f, "#") {
			return false
		}
	}
	return true
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func snippet(b []byte) string {
	const max = 300
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
