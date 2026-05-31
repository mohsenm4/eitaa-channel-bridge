// Package eitaa fetches and parses the public web view of an Eitaa channel.
//
// Eitaa renders channel posts on https://eitaa.com/<channel> as a stream of
// .etme_widget_message_wrap blocks. This package extracts the fields we need
// (id, text, author, date, views, link, photos) from that HTML.
package eitaa

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const (
	defaultBaseURL   = "https://eitaa.com"
	defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36"
)

// Message is a single post parsed from the channel page.
type Message struct {
	ID            int       `json:"id"`
	Channel       string    `json:"channel"`
	Link          string    `json:"link"`
	Author        string    `json:"author,omitempty"`
	ForwardedFrom string    `json:"forwarded_from,omitempty"`
	Date          time.Time `json:"date"`
	Views         int       `json:"views"`
	Text          string    `json:"text"`
	TextHTML      string    `json:"text_html,omitempty"`
	Photos        []string  `json:"photos,omitempty"`
}

// Client fetches and parses channel pages.
type Client struct {
	BaseURL    string
	UserAgent  string
	HTTPClient *http.Client
}

// New returns a Client with sensible defaults.
func New() *Client {
	return &Client{
		BaseURL:   defaultBaseURL,
		UserAgent: defaultUserAgent,
		HTTPClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

// FetchRaw downloads the HTML for the channel page and returns it as a string.
func (c *Client) FetchRaw(ctx context.Context, channel string) (string, error) {
	url := fmt.Sprintf("%s/%s", c.BaseURL, channel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "fa,en;q=0.8")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	return string(body), nil
}

// Fetch downloads the channel page and parses messages from it.
// Messages are returned in ascending order by ID (oldest first).
func (c *Client) Fetch(ctx context.Context, channel string) ([]Message, error) {
	raw, err := c.FetchRaw(ctx, channel)
	if err != nil {
		return nil, err
	}
	return Parse(channel, raw)
}

// Parse extracts messages from a raw channel page HTML string.
func Parse(channel, htmlSrc string) ([]Message, error) {
	doc, err := html.Parse(strings.NewReader(htmlSrc))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	var msgs []Message
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "div" {
			return
		}
		if !hasClass(n, "etme_widget_message") || hasClass(n, "etme_widget_message_wrap") {
			return
		}
		// Only the inner message div carries data-post; this filters out
		// nested helper divs that share the etme_widget_message prefix.
		post := attr(n, "data-post")
		if post == "" {
			return
		}
		msg := parseMessage(channel, n, post)
		msgs = append(msgs, msg)
	})

	// Sort ascending by ID — eitaa.com tends to render oldest first but
	// we sort defensively in case pagination edges change.
	sortByID(msgs)
	return msgs, nil
}

func parseMessage(channel string, root *html.Node, dataPost string) Message {
	msg := Message{
		Channel: channel,
	}

	if idx := strings.LastIndex(dataPost, "/"); idx >= 0 {
		if id, err := strconv.Atoi(dataPost[idx+1:]); err == nil {
			msg.ID = id
		}
	}
	msg.Link = fmt.Sprintf("%s/%s/%d", defaultBaseURL, channel, msg.ID)

	walk(root, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch {
		case n.Data == "a" && hasClass(n, "etme_widget_message_owner_name"):
			msg.Author = strings.TrimSpace(textOf(n))
		case n.Data == "div" && hasClass(n, "etme_widget_message_forwarded_from"):
			msg.ForwardedFrom = cleanText(textOf(n))
		case n.Data == "div" && hasClass(n, "etme_widget_message_text"):
			msg.TextHTML = strings.TrimSpace(innerHTML(n))
			msg.Text = strings.TrimSpace(textOf(n))
		case n.Data == "span" && hasClass(n, "etme_widget_message_views"):
			if v, err := parseViews(attr(n, "data-count"), textOf(n)); err == nil {
				msg.Views = v
			}
		case n.Data == "time" && hasClass(n, "time"):
			if dt := attr(n, "datetime"); dt != "" {
				if t, err := time.Parse(time.RFC3339, dt); err == nil {
					msg.Date = t
				}
			}
		case n.Data == "a" && hasClass(n, "etme_widget_message_photo_wrap"):
			if u := extractBackgroundURL(attr(n, "style")); u != "" {
				msg.Photos = append(msg.Photos, absURL(u))
			}
		case n.Data == "div" && hasClass(n, "etme_widget_message_photo"):
			// Standalone single-photo messages put the URL on this div.
			if u := extractBackgroundURL(attr(n, "style")); u != "" {
				msg.Photos = append(msg.Photos, absURL(u))
			}
		}
	})

	return msg
}

// --- helpers ---

func walk(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	classes := attr(n, "class")
	if classes == "" {
		return false
	}
	for _, c := range strings.Fields(classes) {
		if c == class {
			return true
		}
	}
	return false
}

func textOf(n *html.Node) string {
	var sb strings.Builder
	walk(n, func(c *html.Node) {
		if c.Type == html.TextNode {
			sb.WriteString(c.Data)
		}
	})
	return sb.String()
}

func innerHTML(n *html.Node) string {
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&sb, c); err != nil {
			return ""
		}
	}
	return sb.String()
}

func cleanText(s string) string {
	s = strings.ReplaceAll(s, " ", " ")
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

var bgURLRe = regexp.MustCompile(`background-image:\s*url\(['"]?([^'")]+)['"]?\)`)

func extractBackgroundURL(style string) string {
	m := bgURLRe.FindStringSubmatch(style)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func absURL(u string) string {
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	if strings.HasPrefix(u, "/") {
		return defaultBaseURL + u
	}
	return u
}

var nonDigit = regexp.MustCompile(`[^0-9]`)

// parseViews accepts either the raw data-count attribute or the visible text
// (which may be "۱.۲هزار" etc). We trust data-count if it is a clean integer.
func parseViews(dataCount, fallback string) (int, error) {
	if dataCount != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(dataCount)); err == nil {
			return v, nil
		}
	}
	digits := nonDigit.ReplaceAllString(fallback, "")
	if digits == "" {
		return 0, fmt.Errorf("no digits in views")
	}
	return strconv.Atoi(digits)
}

func sortByID(msgs []Message) {
	for i := 1; i < len(msgs); i++ {
		for j := i; j > 0 && msgs[j-1].ID > msgs[j].ID; j-- {
			msgs[j-1], msgs[j] = msgs[j], msgs[j-1]
		}
	}
}
