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
