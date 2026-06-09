// Package eitaa fetches and parses the public web view of an Eitaa channel.
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
	// ReplyToID is the source-channel ID this post replies to (0 if not a reply); archive-only.
	ReplyToID int `json:"reply_to_id,omitempty"`
}

type Client struct {
	BaseURL    string
	UserAgent  string
	HTTPClient *http.Client
}

func New() *Client {
	return &Client{
		BaseURL:   defaultBaseURL,
		UserAgent: defaultUserAgent,
		HTTPClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (c *Client) FetchRaw(ctx context.Context, channel string) (string, error) {
	return c.fetchRaw(ctx, fmt.Sprintf("%s/%s", c.BaseURL, channel))
}

func (c *Client) FetchRawBefore(ctx context.Context, channel string, beforeID int) (string, error) {
	return c.fetchRaw(ctx, fmt.Sprintf("%s/%s?before=%d", c.BaseURL, channel, beforeID))
}

func (c *Client) fetchRaw(ctx context.Context, url string) (string, error) {
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

// Fetch returns messages sorted ascending by ID.
func (c *Client) Fetch(ctx context.Context, channel string) ([]Message, error) {
	raw, err := c.FetchRaw(ctx, channel)
	if err != nil {
		return nil, err
	}
	return Parse(channel, raw)
}

func (c *Client) FetchBefore(ctx context.Context, channel string, beforeID int) ([]Message, error) {
	raw, err := c.FetchRawBefore(ctx, channel, beforeID)
	if err != nil {
		return nil, err
	}
	return Parse(channel, raw)
}

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
		post := attr(n, "data-post")
		if post == "" {
			return
		}
		msgs = append(msgs, parseMessage(channel, n, post))
	})

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
		case n.Data == "a" && hasClass(n, "etme_widget_message_photo_wrap"),
			n.Data == "div" && hasClass(n, "etme_widget_message_photo"):
			if u := extractBackgroundURL(attr(n, "style")); u != "" {
				msg.Photos = append(msg.Photos, absURL(u))
			}
		case n.Data == "a" && hasClass(n, "etme_widget_message_reply"):
			if id := parseReplyID(attr(n, "href")); id > 0 {
				msg.ReplyToID = id
			}
		}
	})

	return msg
}

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

// parseReplyID extracts the trailing message ID from a reply href like "/channel/123".
func parseReplyID(href string) int {
	if i := strings.LastIndex(href, "/"); i >= 0 {
		if id, err := strconv.Atoi(href[i+1:]); err == nil {
			return id
		}
	}
	return 0
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

// parseViews prefers data-count (clean integer) since visible text loses scale on values like "۱.۲هزار".
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
