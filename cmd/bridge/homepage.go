package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// homepageClient calls the eitaa-bridge-helper plugin endpoints that keep homepage poster slides pointing at the latest post per category.
type homepageClient struct {
	base  string
	user  string
	pass  string
	httpc *http.Client
	// catIDCache memoizes slug→term-ID lookups so reconcilePosterSlides costs one HTTP call per slot
	// instead of two. The runner calls homepageClient from a single goroutine, so no mutex is needed.
	// We never invalidate; the publishing categories don't change at runtime.
	catIDCache map[string]int
}

func newHomepageClient(wpURL, user, pass string) *homepageClient {
	return &homepageClient{
		base: strings.TrimRight(wpURL, "/"),
		user: user,
		pass: pass,
		httpc: &http.Client{
			Timeout: 30 * time.Second,
			// fatemyoon.ir's TLS cert is expired (same workaround as the publisher).
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		},
		catIDCache: map[string]int{},
	}
}

// detectFrontPageID asks WP for the static front-page id; returns 0 when the site uses the blog layout.
func (h *homepageClient) detectFrontPageID(ctx context.Context) (int, error) {
	body, err := h.getJSON(ctx, "/wp-json/eitaa-bridge/v1/site-settings")
	if err != nil {
		return 0, err
	}
	var out struct {
		ShowOnFront string `json:"show_on_front"`
		PageOnFront int    `json:"page_on_front"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("decode site-settings: %w", err)
	}
	if out.ShowOnFront != "page" || out.PageOnFront <= 0 {
		return 0, nil
	}
	return out.PageOnFront, nil
}

// Slide describes one av_slide or av_content_slide returned by /list-slides.
type Slide struct {
	Type    string `json:"type"`
	UID     string `json:"uid"`
	ImageID string `json:"image_id"` // empty for av_content_slide
	Link    string `json:"link"`
	Title   string `json:"title"`
}

// listSlides returns the slide inventory on the given page so the bridge can auto-discover the hashtag→slide_uid mapping.
func (h *homepageClient) listSlides(ctx context.Context, pageID int) ([]Slide, error) {
	body, err := h.getJSON(ctx, fmt.Sprintf("/wp-json/eitaa-bridge/v1/list-slides?page_id=%d", pageID))
	if err != nil {
		return nil, err
	}
	var out struct {
		Slides []Slide `json:"slides"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode list-slides: %w", err)
	}
	return out.Slides, nil
}

// mediaSlug returns the WP media item's slug (e.g. "hemayat-khedmat-poster") used to match poster images against category slugs.
func (h *homepageClient) mediaSlug(ctx context.Context, mediaID int) (string, error) {
	body, err := h.getJSON(ctx, fmt.Sprintf("/wp-json/wp/v2/media/%d", mediaID))
	if err != nil {
		return "", err
	}
	var out struct {
		Slug      string `json:"slug"`
		SourceURL string `json:"source_url"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode media: %w", err)
	}
	if out.Slug != "" {
		return out.Slug, nil
	}
	// Fallback: pull the filename out of source_url, drop extension.
	if i := strings.LastIndex(out.SourceURL, "/"); i >= 0 {
		name := out.SourceURL[i+1:]
		if j := strings.LastIndex(name, "."); j > 0 {
			name = name[:j]
		}
		return name, nil
	}
	return "", nil
}

func (h *homepageClient) getJSON(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(h.user, h.pass)
	req.Header.Set("Accept", "application/json")
	resp, err := h.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, snippet(body))
	}
	return body, nil
}

// updatePosterLink rewrites the av_slide's link='manually,...' to newLink; non-2xx is returned as an error for the caller to log.
func (h *homepageClient) updatePosterLink(ctx context.Context, pageID int, slideUID, newLink string) error {
	body, _ := json.Marshal(map[string]any{
		"page_id": pageID,
		"uid":     slideUID,
		"link":    newLink,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.base+"/wp-json/eitaa-bridge/v1/update-slide-link",
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(h.user, h.pass)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	resp, err := h.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("update-slide-link HTTP %d: %s", resp.StatusCode, snippet(respBody))
	}
	return nil
}

// loadCategoryCache lists every WP category once (paged in chunks of 100) and indexes each one by both its slug
// and its name. We need both because the bridge's config carries a transliterated English Slug (used as a post-
// slug prefix) and a Persian Label (used as the WP term's name); the WP term itself usually has a Persian slug
// distinct from cat.Slug, so a direct slug-equality match against cat.Slug will miss. Same compromise as
// /reconcile-news-section, which accepts "slug or name" via get_category_by_slug + get_term_by('name', ...).
func (h *homepageClient) loadCategoryCache(ctx context.Context) error {
	if len(h.catIDCache) > 0 {
		return nil
	}
	for page := 1; ; page++ {
		body, err := h.getJSON(ctx, fmt.Sprintf(
			"/wp-json/wp/v2/categories?per_page=100&page=%d&_fields=id,slug,name", page))
		if err != nil {
			// WP REST returns 400 with code rest_post_invalid_page_number when paging past the last page;
			// the first call still has to succeed, so only swallow the error if we already loaded some.
			if page > 1 {
				break
			}
			return err
		}
		var cats []struct {
			ID   int    `json:"id"`
			Slug string `json:"slug"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &cats); err != nil {
			return fmt.Errorf("decode categories: %w", err)
		}
		if len(cats) == 0 {
			break
		}
		for _, c := range cats {
			if c.ID <= 0 {
				continue
			}
			if c.Slug != "" {
				h.catIDCache[c.Slug] = c.ID
			}
			if c.Name != "" {
				h.catIDCache[c.Name] = c.ID
			}
		}
		if len(cats) < 100 {
			break
		}
	}
	if len(h.catIDCache) == 0 {
		return fmt.Errorf("no WP categories returned")
	}
	return nil
}

// categoryIDByLabelOrSlug resolves a WP category to its term ID by either its slug or its name. The cache is
// populated lazily on first call.
func (h *homepageClient) categoryIDByLabelOrSlug(ctx context.Context, key string) (int, error) {
	if key == "" {
		return 0, fmt.Errorf("categoryIDByLabelOrSlug: empty key")
	}
	if err := h.loadCategoryCache(ctx); err != nil {
		return 0, err
	}
	if id, ok := h.catIDCache[key]; ok {
		return id, nil
	}
	return 0, fmt.Errorf("category %q not found", key)
}

// newestPostInCategory returns the permalink of the newest published post in the given category.
// found == false (with err == nil) means the category has zero published posts — caller should fall back to the
// archive URL so a homepage slide stays clickable instead of pointing at a trashed post.
// categoryKey is matched against either the WP term's slug or its name (Persian Label works).
func (h *homepageClient) newestPostInCategory(ctx context.Context, categoryKey string) (link string, found bool, err error) {
	id, err := h.categoryIDByLabelOrSlug(ctx, categoryKey)
	if err != nil {
		return "", false, err
	}
	body, gerr := h.getJSON(ctx, fmt.Sprintf(
		"/wp-json/wp/v2/posts?categories=%d&per_page=1&status=publish&orderby=date&order=desc&_fields=link", id))
	if gerr != nil {
		return "", false, gerr
	}
	var out []struct {
		Link string `json:"link"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", false, fmt.Errorf("decode posts: %w", err)
	}
	if len(out) == 0 || out[0].Link == "" {
		return "", false, nil
	}
	return out[0].Link, true, nil
}

// categoryArchiveURL returns the WP category archive URL (used as a slide fallback when the category has no
// published posts). Relies on the categoryIDByLabelOrSlug cache, so when called right after newestPostInCategory
// it makes no extra HTTP call.
func (h *homepageClient) categoryArchiveURL(ctx context.Context, categoryKey string) (string, error) {
	id, err := h.categoryIDByLabelOrSlug(ctx, categoryKey)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/?cat=%d", h.base, id), nil
}

// reconcileNewsSection rebuilds the homepage news cards from the top-3 newest published posts in the given category.
// Called after a delete (so a trashed post never lingers as a card) and safe to call any time — server-side is idempotent.
func (h *homepageClient) reconcileNewsSection(ctx context.Context, pageID int, categorySlug string) error {
	body, _ := json.Marshal(map[string]any{
		"page_id":       pageID,
		"category_slug": categorySlug,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.base+"/wp-json/eitaa-bridge/v1/reconcile-news-section",
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(h.user, h.pass)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	resp, err := h.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("reconcile-news-section HTTP %d: %s", resp.StatusCode, snippet(respBody))
	}
	return nil
}

// errorSnippetMax caps response-body bytes shown in error logs.
const errorSnippetMax = 300

// snippet truncates a response body so error logs stay readable instead of dumping full WP HTML on 500s.
func snippet(b []byte) string {
	if len(b) <= errorSnippetMax {
		return string(b)
	}
	return string(b[:errorSnippetMax]) + "..."
}
