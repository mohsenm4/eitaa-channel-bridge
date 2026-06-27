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

const fakeUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// WordPress publishes a routed message as a WP post via the REST API.
type WordPress struct {
	cfg  config.WordPressTarget
	log  *slog.Logger
	http *http.Client

	catByName   map[string]int // WP category Name → ID, lazy-loaded.
	helperAvail *bool          // tri-state cache: nil unknown, true/false probed.
}

func NewWordPress(cfg config.WordPressTarget, log *slog.Logger) *WordPress {
	if log == nil {
		log = slog.Default()
	}
	// TODO: drop InsecureSkipVerify once fatemyoon.ir renews its TLS cert.
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	log.Warn("wordpress: TLS verification disabled (site cert expired)", "url", cfg.URL)
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

	// Existing post with this slug = prior publish whose HTTP response was lost; adopt instead of duplicating.
	slug := buildPostSlug(msg)
	if existingID, err := p.findPostBySlug(ctx, slug); err != nil {
		p.log.Warn("wordpress: slug lookup failed, proceeding with publish",
			"slug", slug, "err", err)
	} else if existingID > 0 {
		p.log.Info("wordpress: adopting existing post — likely a prior publish whose response was lost",
			"eitaa_id", msg.ID, "wp_id", existingID, "slug", slug, "category", msg.CategoryFa)
		return existingID, nil
	}

	if err := p.loadCategories(ctx); err != nil {
		return 0, fmt.Errorf("load WP categories: %w", err)
	}
	catID, ok := p.catByName[msg.CategoryFa]
	if !ok {
		return 0, fmt.Errorf("WP category %q not found on the site — create it or fix the .env label", msg.CategoryFa)
	}

	featuredID, extraGalleryIDs := p.uploadAllPhotos(ctx, msg)
	sourceID, content := p.renderContent(ctx, msg, catID, featuredID)
	if len(extraGalleryIDs) > 0 {
		content = mergeOrInsertGallery(content, extraGalleryIDs)
	}

	// Preferred: clone via helper plugin so post_meta (WPBakery, theme layout) tags along.
	if sourceID > 0 && p.useHelper(ctx) {
		newID, link, err := p.cloneViaHelper(ctx, sourceID, msg.Title, content, catID, featuredID, slug, msg.Category)
		if err == nil {
			p.log.Info("wordpress: published via clone-post helper",
				"eitaa_id", msg.ID, "wp_id", newID, "source_id", sourceID,
				"status", p.cfg.Status, "category", msg.CategoryFa,
				"title", msg.Title, "slug", slug, "featured_media", featuredID, "link", link)
			return newID, nil
		}
		p.log.Warn("wordpress: clone-post helper failed, falling back to plain insert",
			"eitaa_id", msg.ID, "source_id", sourceID, "err", err)
	}

	return p.plainInsert(ctx, msg, catID, featuredID, slug, content)
}

// Update rewrites an existing WP post to mirror an Eitaa edit; photos are re-uploaded (WP dedupes by hash-slug).
func (p *WordPress) Update(ctx context.Context, postID int, msg router.Routed) error {
	if postID <= 0 {
		return fmt.Errorf("update: invalid postID %d", postID)
	}
	if msg.CategoryFa == "" {
		return fmt.Errorf("update: routed message has no category label")
	}
	if err := p.loadCategories(ctx); err != nil {
		return fmt.Errorf("update: load WP categories: %w", err)
	}
	catID, ok := p.catByName[msg.CategoryFa]
	if !ok {
		return fmt.Errorf("update: WP category %q not found", msg.CategoryFa)
	}

	featuredID, extraGalleryIDs := p.uploadAllPhotos(ctx, msg)
	_, content := p.renderContent(ctx, msg, catID, featuredID)
	if len(extraGalleryIDs) > 0 {
		content = mergeOrInsertGallery(content, extraGalleryIDs)
	}

	payload := map[string]any{
		"title":      msg.Title,
		"content":    content,
		"categories": []int{catID},
	}
	if featuredID != 0 {
		payload["featured_media"] = featuredID
	}
	body, _ := json.Marshal(payload)
	resp, err := p.do(ctx, http.MethodPost, fmt.Sprintf("/wp-json/wp/v2/posts/%d", postID), body)
	if err != nil {
		return fmt.Errorf("update post %d: %w", postID, err)
	}
	var out struct {
		ID   int    `json:"id"`
		Link string `json:"link"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return fmt.Errorf("decode update response: %w (body: %s)", err, snippet(resp))
	}
	p.log.Info("wordpress: updated",
		"eitaa_id", msg.ID, "wp_id", out.ID, "category", msg.CategoryFa,
		"title", msg.Title, "featured_media", featuredID, "link", out.Link)
	return nil
}

// Delete sends the post to trash (force=false). The author can restore from WP if it was a mistake.
func (p *WordPress) Delete(ctx context.Context, postID int) error {
	if postID <= 0 {
		return fmt.Errorf("delete: invalid postID %d", postID)
	}
	_, err := p.do(ctx, http.MethodDelete, fmt.Sprintf("/wp-json/wp/v2/posts/%d", postID), nil)
	if err != nil {
		return fmt.Errorf("delete post %d: %w", postID, err)
	}
	p.log.Info("wordpress: deleted", "wp_id", postID)
	return nil
}

func (p *WordPress) plainInsert(ctx context.Context, msg router.Routed, catID, featuredID int, slug, content string) (int, error) {
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
		"eitaa_id", msg.ID, "wp_id", out.ID, "status", p.cfg.Status,
		"category", msg.CategoryFa, "title", msg.Title,
		"featured_media", featuredID, "link", out.Link)
	return out.ID, nil
}

// uploadAllPhotos uploads photos[0] as featured and the rest as gallery items; failures are non-fatal.
func (p *WordPress) uploadAllPhotos(ctx context.Context, msg router.Routed) (featuredID int, galleryIDs []int) {
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
			galleryIDs = append(galleryIDs, id)
		}
	}
	return featuredID, galleryIDs
}

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

func (p *WordPress) cloneViaHelper(ctx context.Context, sourceID int, title, content string, catID, featuredID int, slug, categorySlug string) (int, string, error) {
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
	// News posts must NOT inherit the template's thumbnail when the Eitaa message has no photo, because
	// the homepage card then shows the template image instead of our generic placeholder. Other categories
	// still inherit the template thumb so report posts without photos keep a category-themed image.
	// String kept in sync with newsCategorySlug in cmd/bridge/run.go.
	if categorySlug == "akhbar-etelaiyeh" {
		payload["inherit_source_thumb"] = false
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

// findPostBySlug returns the ID of a non-trash post with the exact slug, or 0 if none.
func (p *WordPress) findPostBySlug(ctx context.Context, slug string) (int, error) {
	if slug == "" {
		return 0, nil
	}
	path := fmt.Sprintf("/wp-json/wp/v2/posts?slug=%s&status=any&per_page=1", url.QueryEscape(slug))
	body, err := p.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, err
	}
	var items []struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return 0, fmt.Errorf("decode post search: %w (body: %s)", err, snippet(body))
	}
	if len(items) == 0 {
		return 0, nil
	}
	return items[0].ID, nil
}

// buildPostSlug returns "{category}-{eitaa-id}" so WP doesn't derive an ugly Persian-encoded slug.
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

// ─── HTTP plumbing ──────────────────────────────────────────────────

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
	req.Header.Set("User-Agent", fakeUA)
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
		return nil, fmt.Errorf("WP %s %s: HTTP %d: %s", method, path, resp.StatusCode, snippet(respBody))
	}
	return respBody, nil
}

// ─── Categories ─────────────────────────────────────────────────────

type wpCategory struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
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

// ─── Photos / media library ─────────────────────────────────────────

// uploadOrReuse returns an existing media id (matched by hash-based slug) or uploads a new one.
func (p *WordPress) uploadOrReuse(ctx context.Context, photoURL string, fallbackID int) (int, error) {
	filename := photoFilename(photoURL, fallbackID)
	slug := strings.TrimSuffix(filename, "."+extFromURL(photoURL))
	if existingID, _ := p.findMediaBySlug(ctx, slug); existingID > 0 {
		return existingID, nil
	}
	return p.uploadPhoto(ctx, photoURL, filename)
}

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

func (p *WordPress) uploadPhoto(ctx context.Context, photoURL, filename string) (int, error) {
	data, contentType, err := p.downloadPhoto(ctx, photoURL)
	if err != nil {
		return 0, fmt.Errorf("download photo: %w", err)
	}

	data, contentType, err = maybeCompressImage(data, contentType, maxPhotoBytes, p.log)
	if err != nil {
		return 0, fmt.Errorf("compress photo: %w", err)
	}

	// When a PNG was converted to JPEG, fix the filename extension to match.
	if contentType == "image/jpeg" && strings.HasSuffix(filename, ".png") {
		filename = strings.TrimSuffix(filename, ".png") + ".jpg"
	}

	return p.uploadMedia(ctx, data, contentType, filename)
}

func (p *WordPress) downloadPhoto(ctx context.Context, photoURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, photoURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", fakeUA)
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
	req.Header.Set("User-Agent", fakeUA)
	resp, err := p.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("upload media: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("WP POST /wp-json/wp/v2/media: HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	var out struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("decode media response: %w (body: %s)", err, snippet(body))
	}
	return out.ID, nil
}

// photoFilename keys filenames by Eitaa's content hash so the same photo dedupes across messages.
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

// eitaaPhotoHash extracts the content hash from https://eitaa.com/download_<hash>?token=… URLs.
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

// ─── Content rendering ──────────────────────────────────────────────

var (
	vcColumnTextRe  = regexp.MustCompile(`(?s)\[vc_column_text\].*?\[/vc_column_text\]`)
	vcSingleImageRe = regexp.MustCompile(`(\[vc_single_image[^\]]*?image=")(\d+)(")`)
	vcGalleryRe     = regexp.MustCompile(`\[vc_gallery([^\]]*?)images="([^"]*)"([^\]]*?)\]`)
)

// renderContent picks a layout in priority order: bundled template → clone source post → plain wrap.
func (p *WordPress) renderContent(ctx context.Context, msg router.Routed, catID, featuredID int) (int, string) {
	sourceID, raw, err := p.fetchTemplate(ctx, catID)
	if err != nil {
		p.log.Warn("wordpress: template fetch failed, using simple wrap",
			"category", msg.CategoryFa, "err", err)
		return 0, p.renderHTML(msg)
	}
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

// fetchTemplate returns the id and raw shortcodes of the latest published post in catID.
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

// applyTemplate swaps only the last [vc_column_text] body and first vc_single_image id, keeping the rest of the template verbatim.
func (p *WordPress) applyTemplate(templateRaw string, msg router.Routed, featuredID int) (string, bool) {
	out, ok := replaceLastColumnText(templateRaw, p.renderBodyLines(msg))
	if !ok {
		return "", false
	}
	if featuredID > 0 {
		out = replaceFirstSingleImageID(out, featuredID)
	}
	return out, true
}

// replaceLastColumnText swaps the last [vc_column_text]...[/vc_column_text] block with newBody; returns false when no block is found.
func replaceLastColumnText(template, newBody string) (string, bool) {
	matches := vcColumnTextRe.FindAllStringIndex(template, -1)
	if len(matches) == 0 {
		return "", false
	}
	last := matches[len(matches)-1]
	newBlock := "[vc_column_text]\n" + newBody + "[/vc_column_text]"
	return template[:last[0]] + newBlock + template[last[1]:], true
}

// replaceFirstSingleImageID rewrites the image id in the first [vc_single_image] tag and leaves the rest untouched.
func replaceFirstSingleImageID(content string, imageID int) string {
	loc := vcSingleImageRe.FindStringIndex(content)
	if loc == nil {
		return content
	}
	match := content[loc[0]:loc[1]]
	newMatch := vcSingleImageRe.ReplaceAllString(match, fmt.Sprintf(`${1}%d${3}`, imageID))
	return content[:loc[0]] + newMatch + content[loc[1]:]
}

func (p *WordPress) renderHTML(msg router.Routed) string {
	return wrapVCRow(p.renderBodyLines(msg))
}

// renderBodyLines emits one <p> per content line, dropping markers, hashtags, and the channel sign-off block.
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
		if isChannelSignatureLine(line) {
			continue
		}
		if isHashtagOnlyLine(line) {
			break
		}
		inner.WriteString("<p>")
		inner.WriteString(htmlEscape(line))
		inner.WriteString("</p>\n")
	}
	return inner.String()
}

// isChannelSignatureLine matches the Eitaa channel sign-off footer (🟢/🆔 prefix or any eitaa.com URL).
func isChannelSignatureLine(line string) bool {
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "🟢") || strings.HasPrefix(line, "🆔") {
		return true
	}
	if strings.Contains(line, "eitaa.com/") {
		return true
	}
	return false
}

func wrapVCRow(html string) string {
	return "[vc_row][vc_column][vc_column_text]\n" + html + "[/vc_column_text][/vc_column][/vc_row]\n"
}

// ─── Gallery insertion ──────────────────────────────────────────────

// mergeOrInsertGallery splices new media ids into an existing [vc_gallery] or inserts a fresh one before [/vc_column][/vc_row].
func mergeOrInsertGallery(content string, newIDs []int) string {
	gallery := buildVCGalleryInline(newIDs)
	if gallery == "" {
		return content
	}
	if m := vcGalleryRe.FindStringSubmatchIndex(content); m != nil {
		existing := content[m[4]:m[5]]
		return content[:m[4]] + mergeIDList(existing, newIDs) + content[m[5]:]
	}
	if idx := strings.LastIndex(content, "[/vc_column][/vc_row]"); idx >= 0 {
		return content[:idx] + gallery + content[idx:]
	}
	return content + "\n[vc_row][vc_column]" + gallery + "[/vc_column][/vc_row]\n"
}

func buildVCGalleryInline(mediaIDs []int) string {
	if len(mediaIDs) == 0 {
		return ""
	}
	ids := make([]string, len(mediaIDs))
	for i, id := range mediaIDs {
		ids[i] = fmt.Sprintf("%d", id)
	}
	return fmt.Sprintf(`[vc_gallery interval="3" images="%s" img_size="full" onclick=""]`, strings.Join(ids, ","))
}

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

// ─── Misc helpers ───────────────────────────────────────────────────

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
