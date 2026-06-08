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
	"net/url"
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

	// Photos[0] → featured image. Photos[1..N] → gallery on the same
	// post, so an Eitaa media-group (album) flows straight into a
	// slider without the author having to send a separate #آرشیو
	// reply. Failures here are non-fatal: better to publish text-only
	// than to stall the whole message.
	var featuredID int
	var extraGalleryIDs []int
	for i, photo := range msg.Photos {
		id, err := p.uploadOrReuse(ctx, photo, msg.ID)
		if err != nil {
			p.log.Warn("wordpress: photo upload failed — continuing without",
				"eitaa_id", msg.ID, "photo_idx", i, "photo", photo, "err", err)
			continue
		}
		if i == 0 {
			featuredID = id
		} else {
			extraGalleryIDs = append(extraGalleryIDs, id)
		}
	}

	sourceID, content := p.renderContent(ctx, msg, catID, featuredID)
	if len(extraGalleryIDs) > 0 {
		content = mergeOrInsertGallery(content, extraGalleryIDs)
	}
	slug := buildPostSlug(msg)

	// Preferred path: ask the eitaa-bridge-helper plugin to clone the
	// source post (so all its post_meta — WPBakery state, theme layout —
	// comes along), then override the visible fields. Falls back to a
	// plain REST insert if the helper isn't installed.
	if sourceID > 0 && p.useHelper(ctx) {
		newID, link, err := p.cloneViaHelper(ctx, sourceID, msg.Title, content, catID, featuredID, slug)
		if err == nil {
			p.log.Info("wordpress: published via clone-post helper",
				"eitaa_id", msg.ID, "wp_id", newID, "source_id", sourceID,
				"status", p.cfg.Status, "category", msg.CategoryFa,
				"title", msg.Title, "slug", slug, "featured_media", featuredID, "link", link)
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
		"slug":       slug,
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
func (p *WordPress) cloneViaHelper(ctx context.Context, sourceID int, title, content string, catID, featuredID int, slug string) (int, string, error) {
	payload := map[string]any{
		"source_id": sourceID,
		"title":     title,
		"content":   content,
		"status":    p.cfg.Status,
		"category":  catID,
		"slug":      slug,
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

// buildPostSlug builds a short, ASCII-only WP slug from the routed
// message: "{category-slug}-{eitaa-id}", e.g. "rezvan-12345". WP would
// otherwise auto-derive the slug from the Persian title and produce
// ugly URL-encoded paths like /گزارش-سبدکالا-…/. The category slug is
// already lowercase ASCII (set by config), and the eitaa id makes the
// slug unique per source message — so re-running the bridge on the
// same message keeps the URL stable.
func buildPostSlug(msg router.Routed) string {
	cat := strings.ToLower(strings.TrimSpace(msg.Category))
	if cat == "" {
		cat = "post"
	}
	if msg.ID > 0 {
		return fmt.Sprintf("%s-%d", cat, msg.ID)
	}
	return cat
}

var vcGalleryRe = regexp.MustCompile(`\[vc_gallery([^\]]*?)images="([^"]*)"([^\]]*?)\]`)

// mergeOrInsertGallery places the new media ids in the post:
//   - if a [vc_gallery] already exists, the new ids are appended to its
//     images="…" list (deduped, original order preserved)
//   - otherwise the gallery is inserted just before the final
//     [/vc_column][/vc_row] of the body so it sits inside the existing
//     row (no extra section padding) — matching how the site's
//     hand-built توانمندسازی posts arrange their gallery
//   - if neither marker is present (unusual content shape) we fall back
//     to a standalone row appended at the end
func mergeOrInsertGallery(content string, newIDs []int) string {
	gallery := buildVCGalleryInline(newIDs)
	if gallery == "" {
		return content
	}

	if m := vcGalleryRe.FindStringSubmatchIndex(content); m != nil {
		existing := content[m[4]:m[5]]
		merged := mergeIDList(existing, newIDs)
		return content[:m[4]] + merged + content[m[5]:]
	}

	if idx := strings.LastIndex(content, "[/vc_column][/vc_row]"); idx >= 0 {
		return content[:idx] + gallery + content[idx:]
	}

	return content + "\n[vc_row][vc_column]" + gallery + "[/vc_column][/vc_row]\n"
}

// buildVCGalleryInline returns just the [vc_gallery …] shortcode, with
// no surrounding row/column — caller decides where to splice it.
func buildVCGalleryInline(mediaIDs []int) string {
	if len(mediaIDs) == 0 {
		return ""
	}
	ids := make([]string, len(mediaIDs))
	for i, id := range mediaIDs {
		ids[i] = fmt.Sprintf("%d", id)
	}
	return fmt.Sprintf(
		`[vc_gallery interval="3" images="%s" img_size="full" onclick=""]`,
		strings.Join(ids, ","))
}

// mergeIDList merges newIDs into a comma-separated existing id string,
// dropping duplicates while preserving original order then appending
// novel ids in insertion order.
func mergeIDList(existing string, newIDs []int) string {
	seen := make(map[string]bool)
	var out []string
	for _, id := range strings.Split(existing, ",") {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, id := range newIDs {
		s := fmt.Sprintf("%d", id)
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

// uploadOrReuse uploads photoURL to the media library, or returns the
// id of an existing item with the same hash-based filename. fallbackID
// only matters if the URL has no Eitaa hash (very old scrapes).
//
// This is the single point where the bot decides "have we seen this
// exact photo before?" — used for both the featured image and the
// gallery photos so the same shot never lands in the library twice.
func (p *WordPress) uploadOrReuse(ctx context.Context, photoURL string, fallbackID int) (int, error) {
	filename := photoFilename(photoURL, fallbackID)
	slug := strings.TrimSuffix(filename, "."+extFromURL(photoURL))
	if existingID, _ := p.findMediaBySlug(ctx, slug); existingID > 0 {
		return existingID, nil
	}
	return p.uploadPhoto(ctx, photoURL, filename)
}

// findMediaBySlug returns the id of an existing media library item with
// the given slug, or 0 if none exists. Slug is derived from the
// hash-based filename, so a republish or a media-group sent twice
// hits the existing item instead of creating a duplicate.
func (p *WordPress) findMediaBySlug(ctx context.Context, slug string) (int, error) {
	path := fmt.Sprintf("/wp-json/wp/v2/media?slug=%s&per_page=1", url.QueryEscape(slug))
	body, err := p.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, err
	}
	var items []struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return 0, fmt.Errorf("decode media search: %w", err)
	}
	if len(items) == 0 {
		return 0, nil
	}
	return items[0].ID, nil
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

// photoFilename builds a stable filename for a single Eitaa photo, keyed
// by the photo's content hash that Eitaa puts in the download URL.
// Same photo across messages → same filename → same media library slug
// → dedup'd at upload time. This is what makes #آرشیو replays and
// cross-message photo overlap not produce duplicates in the gallery.
//
// fallbackID is used when the URL has no recognisable hash (very old
// scrapes), so we still produce a unique-enough name.
func photoFilename(photoURL string, fallbackID int) string {
	ext := extFromURL(photoURL)
	if ext == "" {
		ext = "jpg"
	}
	if hash := eitaaPhotoHash(photoURL); hash != "" {
		return fmt.Sprintf("eitaa-photo-%s.%s", hash, ext)
	}
	return fmt.Sprintf("eitaa-photo-fallback-%d.%s", fallbackID, ext)
}

// eitaaPhotoHash pulls the content hash from an Eitaa download URL.
// Format: https://eitaa.com/download_<hash>?token=… — the token rotates
// across fetches but the hash stays constant for the same photo.
func eitaaPhotoHash(photoURL string) string {
	const marker = "download_"
	i := strings.Index(photoURL, marker)
	if i < 0 {
		return ""
	}
	rest := photoURL[i+len(marker):]
	if q := strings.IndexAny(rest, "?#"); q >= 0 {
		rest = rest[:q]
	}
	return rest
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

// renderContent picks the best layout for this post, in priority order:
//  1. If a bundled per-category template exists (internal/publisher/
//     templates/<slug>.tmpl) it's filled with placeholders extracted
//     from the channel message. Source post is still fetched so the
//     helper plugin can copy its meta (layout, WPBakery).
//  2. Otherwise, clone the latest published post's raw shortcode tree
//     and swap the last [vc_column_text] body + first vc_single_image
//     id with the new content / featured media.
//  3. Otherwise, a plain WPBakery row/column wrap around the body.
//
// Returns the source post id (0 if no source post was found) and the
// rendered content. The id lets Publish hand it off to the helper
// plugin so all the source's post_meta tags along.
func (p *WordPress) renderContent(ctx context.Context, msg router.Routed, catID, featuredID int) (int, string) {
	sourceID, raw, err := p.fetchTemplate(ctx, catID)
	if err != nil {
		p.log.Warn("wordpress: template fetch failed, using simple wrap",
			"category", msg.CategoryFa, "err", err)
		return 0, p.renderHTML(msg)
	}

	// Preferred path: per-category template file. Doesn't need the
	// source raw, only its id (for the helper plugin).
	if tmpl, ok := loadCategoryTemplate(msg.Category); ok {
		if vars := extractCategoryVars(msg, featuredID); vars != nil {
			out := renderCategoryTemplate(tmpl, vars)
			p.log.Info("wordpress: rendered with bundled category template",
				"category", msg.CategoryFa, "slug", msg.Category, "source_id", sourceID)
			return sourceID, out
		}
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
	p.log.Info("wordpress: rendered with source-post clone",
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
