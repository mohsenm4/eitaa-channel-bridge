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
	"regexp"
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

	// Tri-state cache for the eitaa-bridge-helper plugin:
	//   nil   = not probed yet
	//   true  = endpoint exists, use it for cloning
	//   false = endpoint missing, fall back to plain REST insert
	helperAvail *bool
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

func (p *WordPress) Publish(ctx context.Context, msg router.Routed) (int, error) {
	if msg.IsArchive() {
		return 0, p.publishArchive(ctx, msg)
	}
	if msg.CategoryFa == "" {
		return 0, fmt.Errorf("routed message has no category label")
	}
	if err := p.loadCategories(ctx); err != nil {
		return 0, fmt.Errorf("load WP categories: %w", err)
	}
	catID, ok := p.catByName[msg.CategoryFa]
	if !ok {
		return 0, fmt.Errorf("WP category %q not found on the site — create it or fix the .env label", msg.CategoryFa)
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

	sourceID, content := p.renderContent(ctx, msg, catID, featuredID)

	// Preferred path: ask the eitaa-bridge-helper plugin to clone the
	// source post (so all its post_meta — WPBakery state, theme layout —
	// comes along), then override the visible fields. Falls back to a
	// plain REST insert if the helper isn't installed.
	if sourceID > 0 && p.useHelper(ctx) {
		newID, link, err := p.cloneViaHelper(ctx, sourceID, msg.Title, content, catID, featuredID)
		if err == nil {
			p.log.Info("wordpress: published via clone-post helper",
				"eitaa_id", msg.ID, "wp_id", newID, "source_id", sourceID,
				"status", p.cfg.Status, "category", msg.CategoryFa,
				"title", msg.Title, "featured_media", featuredID, "link", link)
			return newID, nil
		}
		// Helper exists but failed for this call — log and fall through.
		p.log.Warn("wordpress: clone-post helper failed, falling back to plain insert",
			"eitaa_id", msg.ID, "source_id", sourceID, "err", err)
	}

	payload := map[string]any{
		"title":      msg.Title,
		"content":    content,
		"status":     p.cfg.Status,
		"categories": []int{catID},
	}
	if featuredID != 0 {
		payload["featured_media"] = featuredID
	}
	body, _ := json.Marshal(payload)

	resp, err := p.do(ctx, http.MethodPost, "/wp-json/wp/v2/posts", body)
	if err != nil {
		return 0, err
	}

	var out struct {
		ID   int    `json:"id"`
		Link string `json:"link"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return 0, fmt.Errorf("decode WP post response: %w (body: %s)", err, snippet(resp))
	}

	p.log.Info("wordpress: published",
		"eitaa_id", msg.ID,
		"wp_id", out.ID,
		"status", p.cfg.Status,
		"category", msg.CategoryFa,
		"title", msg.Title,
		"featured_media", featuredID,
		"link", out.Link,
	)
	return out.ID, nil
}

// useHelper returns true once the eitaa-bridge-helper REST route is
// confirmed to exist on the target. The result is cached so we don't
// hit the index endpoint on every Publish.
func (p *WordPress) useHelper(ctx context.Context) bool {
	if p.helperAvail != nil {
		return *p.helperAvail
	}
	body, err := p.do(ctx, http.MethodGet, "/wp-json/eitaa-bridge/v1", nil)
	avail := err == nil && len(body) > 0
	p.helperAvail = &avail
	if avail {
		p.log.Info("wordpress: eitaa-bridge-helper plugin detected")
	} else {
		p.log.Info("wordpress: eitaa-bridge-helper plugin not installed — using plain REST insert")
	}
	return avail
}

// cloneViaHelper POSTs to the helper plugin's /clone-post endpoint,
// which copies all post meta from sourceID and then overrides the
// visible fields. Returns the new post's id and link.
func (p *WordPress) cloneViaHelper(ctx context.Context, sourceID int, title, content string, catID, featuredID int) (int, string, error) {
	payload := map[string]any{
		"source_id": sourceID,
		"title":     title,
		"content":   content,
		"status":    p.cfg.Status,
		"category":  catID,
	}
	if featuredID > 0 {
		payload["featured_media"] = featuredID
	}
	body, _ := json.Marshal(payload)
	resp, err := p.do(ctx, http.MethodPost, "/wp-json/eitaa-bridge/v1/clone-post", body)
	if err != nil {
		return 0, "", err
	}
	var out struct {
		ID   int    `json:"id"`
		Link string `json:"link"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return 0, "", fmt.Errorf("decode clone-post response: %w (body: %s)", err, snippet(resp))
	}
	if out.ID == 0 {
		return 0, "", fmt.Errorf("clone-post returned no id (body: %s)", snippet(resp))
	}
	return out.ID, out.Link, nil
}

// publishArchive uploads the photos from a #آرشیو follow-up and attaches
// them to msg.ParentPostID (resolved by the runner from the reply chain
// via state). The post's content is extended with <img> tags so the
// gallery shows up in the draft preview without admin intervention.
func (p *WordPress) publishArchive(ctx context.Context, msg router.Routed) error {
	if msg.ParentPostID == 0 {
		return fmt.Errorf("archive message %d has no parent post in state — was it sent as a reply to a published post?", msg.ID)
	}
	if len(msg.Photos) == 0 {
		p.log.Info("wordpress: archive message has no photos — nothing to attach",
			"eitaa_id", msg.ID, "wp_post", msg.ParentPostID)
		return nil
	}

	parentID := msg.ParentPostID
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
		// Wrap the gallery in its own WPBakery row so it sits in the
		// same grid as the body (and any later galleries) instead of
		// breaking out into raw HTML.
		extra := "\n" + wrapVCRow(strings.Join(imgTags, "\n")+"\n")
		if err := p.appendToPostContent(ctx, parentID, extra); err != nil {
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

// renderContent picks the best layout for this post:
//   - if the target category has an existing published post, clone its
//     raw shortcode structure verbatim and only swap the last
//     [vc_column_text] body and the first [vc_single_image] id
//   - otherwise fall back to a simple WPBakery wrap so the post still
//     sits in the right column grid
//
// Returns the source post id (0 if no template was found) and the
// rendered content. The id lets Publish hand it off to the
// clone-post helper plugin so all the source's post_meta tags along.
//
// Errors fetching the template are logged and degrade to the fallback;
// publishing should never block on a missing template.
func (p *WordPress) renderContent(ctx context.Context, msg router.Routed, catID, featuredID int) (int, string) {
	sourceID, raw, err := p.fetchTemplate(ctx, catID)
	if err != nil {
		p.log.Warn("wordpress: template fetch failed, using simple wrap",
			"category", msg.CategoryFa, "err", err)
		return 0, p.renderHTML(msg)
	}
	if raw == "" {
		p.log.Info("wordpress: no template post in category, using simple wrap",
			"category", msg.CategoryFa)
		return 0, p.renderHTML(msg)
	}
	out, ok := p.applyTemplate(raw, msg, featuredID)
	if !ok {
		p.log.Warn("wordpress: template did not match expected shape, using simple wrap",
			"category", msg.CategoryFa)
		return 0, p.renderHTML(msg)
	}
	p.log.Info("wordpress: rendered with category template",
		"category", msg.CategoryFa, "source_id", sourceID, "template_chars", len(raw))
	return sourceID, out
}

// fetchTemplate returns the id and raw content of the latest published
// post in the given category, or (0, "", nil) if there isn't one. Uses
// context=edit so WP returns the unprocessed shortcodes (raw), not the
// rendered HTML.
func (p *WordPress) fetchTemplate(ctx context.Context, catID int) (int, string, error) {
	path := fmt.Sprintf(
		"/wp-json/wp/v2/posts?categories=%d&per_page=1&orderby=date&order=desc&status=publish&context=edit",
		catID)
	body, err := p.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, "", err
	}
	var posts []struct {
		ID      int `json:"id"`
		Content struct {
			Raw string `json:"raw"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &posts); err != nil {
		return 0, "", fmt.Errorf("decode template list: %w (body: %s)", err, snippet(body))
	}
	if len(posts) == 0 {
		return 0, "", nil
	}
	return posts[0].ID, posts[0].Content.Raw, nil
}

// vcColumnTextRe matches a single [vc_column_text]…[/vc_column_text] block,
// non-greedy and across newlines.
var vcColumnTextRe = regexp.MustCompile(`(?s)\[vc_column_text\].*?\[/vc_column_text\]`)

// vcSingleImageRe matches the image="…" attribute of a vc_single_image
// shortcode (just the attribute portion so we can swap the id in place).
var vcSingleImageRe = regexp.MustCompile(`(\[vc_single_image[^\]]*?image=")(\d+)(")`)

// applyTemplate clones the previous post's shortcode tree verbatim and
// only swaps:
//   - the LAST [vc_column_text] block's inner content with the new body
//     (the last block is, by convention on this site, the per-post
//     report/text; earlier blocks tend to be static preamble like the
//     opening hadith)
//   - the image id of the FIRST [vc_single_image] with the new featured
//     media id (when featuredID > 0)
//
// Everything else — rows, columns, separators, dividers — stays exactly
// as it was so the new post lays out identically to its template.
//
// Returns (content, true) on success, ("", false) if the template
// doesn't contain a recognisable [vc_column_text] block (caller falls
// back to the simple wrap).
func (p *WordPress) applyTemplate(templateRaw string, msg router.Routed, featuredID int) (string, bool) {
	matches := vcColumnTextRe.FindAllStringIndex(templateRaw, -1)
	if len(matches) == 0 {
		return "", false
	}
	last := matches[len(matches)-1]
	newBlock := "[vc_column_text]\n" + p.renderBodyLines(msg) + "[/vc_column_text]"
	out := templateRaw[:last[0]] + newBlock + templateRaw[last[1]:]

	if featuredID > 0 {
		replaced := false
		out = vcSingleImageRe.ReplaceAllStringFunc(out, func(match string) string {
			if replaced {
				return match
			}
			replaced = true
			return vcSingleImageRe.ReplaceAllString(match,
				fmt.Sprintf(`${1}%d${3}`, featuredID))
		})
	}
	return out, true
}

// renderHTML is the no-template fallback: a single WPBakery row+column
// wrap around the body lines.
func (p *WordPress) renderHTML(msg router.Routed) string {
	return wrapVCRow(p.renderBodyLines(msg))
}

// renderBodyLines turns the channel message into the inner HTML used
// inside [vc_column_text]: the event date (if present) and one <p> per
// content line, with marker lines and hashtag-only lines stripped.
func (p *WordPress) renderBodyLines(msg router.Routed) string {
	var inner strings.Builder
	if msg.EventDate != "" {
		inner.WriteString("<p>📅 ")
		inner.WriteString(htmlEscape(msg.EventDate))
		inner.WriteString("</p>\n")
	}
	for _, raw := range strings.Split(msg.Text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line == "." {
			continue
		}
		if hasAnyPrefix(line, router.TitleMarker, router.DateMarker, router.SubtitleMarker) {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, router.BodyMarker))
		if line == "" {
			continue
		}
		if isHashtagOnlyLine(line) {
			continue
		}
		inner.WriteString("<p>")
		inner.WriteString(htmlEscape(line))
		inner.WriteString("</p>\n")
	}
	return inner.String()
}

// wrapVCRow wraps arbitrary HTML in a WPBakery row+column+column_text so
// the content sits in the same layout grid as native site posts. Used
// for the no-template fallback and for #آرشیو photo blocks.
func wrapVCRow(html string) string {
	return "[vc_row][vc_column][vc_column_text]\n" +
		html +
		"[/vc_column_text][/vc_column][/vc_row]\n"
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
