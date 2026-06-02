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

	// In-memory: the WP post ID of the most recently published post.
	// Archive (#آرشیو) follow-ups attach their photos to this post.
	// Resets to 0 on restart — initial archive messages after restart
	// log a warning and skip.
	lastPostID int
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
	if msg.IsArchive() {
		return p.publishArchive(ctx, msg)
	}
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

	// Upload the first photo (if any) as the featured image. A failure
	// here is non-fatal: better to publish without the image than to
	// stall the whole message.
	var featuredID int
	if len(msg.Photos) > 0 {
		id, err := p.uploadPhoto(ctx, msg.Photos[0], featuredFilename(msg))
		if err != nil {
			p.log.Warn("wordpress: featured image upload failed — publishing without",
				"eitaa_id", msg.ID, "photo", msg.Photos[0], "err", err)
		} else {
			featuredID = id
		}
	}

	payload := map[string]any{
		"title":      msg.Title,
		"content":    p.renderHTML(msg),
		"status":     p.cfg.Status,
		"categories": []int{catID},
	}
	if featuredID != 0 {
		payload["featured_media"] = featuredID
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

	p.lastPostID = out.ID

	p.log.Info("wordpress: published",
		"eitaa_id", msg.ID,
		"wp_id", out.ID,
		"status", p.cfg.Status,
		"category", msg.CategoryFa,
		"title", msg.Title,
		"featured_media", featuredID,
		"link", out.Link,
	)
	return nil
}

// publishArchive uploads the photos from a #آرشیو follow-up and attaches
// them to the most recently published post. The post's content is also
// extended with <img> tags so the gallery shows up in the draft preview
// without admin intervention.
func (p *WordPress) publishArchive(ctx context.Context, msg router.Routed) error {
	if p.lastPostID == 0 {
		return fmt.Errorf("archive message %d arrived with no preceding post in this session — skipping", msg.ID)
	}
	if len(msg.Photos) == 0 {
		p.log.Info("wordpress: archive message has no photos — nothing to attach",
			"eitaa_id", msg.ID, "wp_post", p.lastPostID)
		return nil
	}

	parentID := p.lastPostID
	var imgTags []string
	var uploaded []int
	for i, photo := range msg.Photos {
		filename := fmt.Sprintf("eitaa-%s-%d-archive-%d.jpg", msg.Channel, msg.ID, i+1)
		mediaID, err := p.uploadPhoto(ctx, photo, filename)
		if err != nil {
			p.log.Warn("wordpress: archive photo upload failed",
				"eitaa_id", msg.ID, "photo", photo, "err", err)
			continue
		}
		if err := p.attachMediaToPost(ctx, mediaID, parentID); err != nil {
			p.log.Warn("wordpress: archive media attach failed",
				"eitaa_id", msg.ID, "media", mediaID, "post", parentID, "err", err)
			continue
		}
		uploaded = append(uploaded, mediaID)
		if src := p.mediaSourceURL(ctx, mediaID); src != "" {
			imgTags = append(imgTags, fmt.Sprintf(`<p><img src=%q alt=""/></p>`, src))
		}
	}

	if len(imgTags) > 0 {
		if err := p.appendToPostContent(ctx, parentID, "\n"+strings.Join(imgTags, "\n")); err != nil {
			p.log.Warn("wordpress: appending archive imgs to post failed",
				"post", parentID, "err", err)
		}
	}

	p.log.Info("wordpress: archive attached",
		"eitaa_id", msg.ID,
		"wp_post", parentID,
		"photos", len(uploaded),
	)
	return nil
}

func (p *WordPress) attachMediaToPost(ctx context.Context, mediaID, postID int) error {
	body, _ := json.Marshal(map[string]any{"post": postID})
	path := fmt.Sprintf("/wp-json/wp/v2/media/%d", mediaID)
	_, err := p.do(ctx, http.MethodPost, path, body)
	return err
}

// mediaSourceURL fetches the public URL of a media item. Falls back to
// empty string on error — the caller treats that as "skip the img tag"
// rather than failing the whole archive flow.
func (p *WordPress) mediaSourceURL(ctx context.Context, mediaID int) string {
	path := fmt.Sprintf("/wp-json/wp/v2/media/%d", mediaID)
	body, err := p.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return ""
	}
	var out struct {
		SourceURL string `json:"source_url"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return ""
	}
	return out.SourceURL
}

// appendToPostContent reads the current post content and PATCHes it
// with the original content + appended HTML. WP REST has no append
// primitive so we do read-modify-write; that's fine for drafts.
func (p *WordPress) appendToPostContent(ctx context.Context, postID int, extra string) error {
	path := fmt.Sprintf("/wp-json/wp/v2/posts/%d?context=edit", postID)
	body, err := p.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	var current struct {
		Content struct {
			Raw string `json:"raw"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &current); err != nil {
		return fmt.Errorf("decode post: %w", err)
	}

	updated, _ := json.Marshal(map[string]any{
		"content": current.Content.Raw + extra,
	})
	_, err = p.do(ctx, http.MethodPost, fmt.Sprintf("/wp-json/wp/v2/posts/%d", postID), updated)
	return err
}

// uploadPhoto downloads a photo from Eitaa and uploads it to WP's
// media library. Returns the new media ID for use as featured_media.
func (p *WordPress) uploadPhoto(ctx context.Context, photoURL, filename string) (int, error) {
	data, contentType, err := p.downloadPhoto(ctx, photoURL)
	if err != nil {
		return 0, fmt.Errorf("download photo: %w", err)
	}
	return p.uploadMedia(ctx, data, contentType, filename)
}

func (p *WordPress) downloadPhoto(ctx context.Context, photoURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, photoURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("download %s: HTTP %d", photoURL, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" || ct == "application/octet-stream" {
		ct = http.DetectContentType(data)
	}
	return data, ct, nil
}

func (p *WordPress) uploadMedia(ctx context.Context, data []byte, contentType, filename string) (int, error) {
	url := strings.TrimRight(p.cfg.URL, "/") + "/wp-json/wp/v2/media"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(p.cfg.Username, p.cfg.AppPassword)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := p.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("upload media: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("WP POST /wp-json/wp/v2/media: HTTP %d: %s",
			resp.StatusCode, snippet(body))
	}
	var out struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("decode media response: %w (body: %s)", err, snippet(body))
	}
	return out.ID, nil
}

func featuredFilename(msg router.Routed) string {
	ext := "jpg"
	if len(msg.Photos) > 0 {
		if e := extFromURL(msg.Photos[0]); e != "" {
			ext = e
		}
	}
	return fmt.Sprintf("eitaa-%s-%d.%s", msg.Channel, msg.ID, ext)
}

func extFromURL(u string) string {
	// strip query
	if i := strings.Index(u, "?"); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndex(u, "."); i >= 0 {
		ext := strings.ToLower(u[i+1:])
		switch ext {
		case "jpg", "jpeg", "png", "gif", "webp":
			return ext
		}
	}
	return ""
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
	if msg.EventDate != "" {
		head.WriteString("<p>📅 ")
		head.WriteString(htmlEscape(msg.EventDate))
		head.WriteString("</p>\n")
	}

	var body strings.Builder
	for _, raw := range strings.Split(msg.Text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line == "." {
			continue
		}
		// Drop the marker lines whose values are already in head/title.
		if hasAnyPrefix(line, router.TitleMarker, router.DateMarker, router.SubtitleMarker) {
			continue
		}
		// 📝 is a hint for readers; strip it but keep the content.
		line = strings.TrimSpace(strings.TrimPrefix(line, router.BodyMarker))
		if line == "" {
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

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
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
