// Package router classifies a parsed Eitaa message into a site category
// and extracts the display fields (title, subtitle, event date) that the
// channel posting guide describes.
//
// The list of categories and the default-bucket policy come from
// config.Publishing; only the marker prefixes (🔻 / 🟩 / 🗓) and the
// fallback-title heuristics live in code.
//
// See docs/posting-guide.md for the rules this implements.
package router

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
)

// Routed is an Eitaa message with site-routing metadata attached.
type Routed struct {
	eitaa.Message

	Category   string   `json:"category,omitempty"`    // machine slug; empty = unclassified
	CategoryFa string   `json:"category_fa,omitempty"` // Persian label for UI
	Title      string   `json:"title"`                 // display title
	Subtitle   string   `json:"subtitle,omitempty"`    // optional subtitle
	EventDate  string   `json:"event_date,omitempty"`  // Persian date string (free-form)
	Hashtags   []string `json:"hashtags"`              // every #tag found in the message
}

// Marker prefixes the channel posting guide tells admins to use.
const (
	TitleMarker    = "🔻"
	SubtitleMarker = "🟩"
	DateMarker     = "🗓"

	titleMaxLen = 80
)

// Router classifies messages for a single channel.
type Router struct {
	channelNameRe *regexp.Regexp
	categories    []config.Category
	defaultCat    *config.Category
}

// New builds a Router for the given channel and category set.
// Categories come from config; the channel name is used to recognise
// self-reference lines when picking a fallback title.
func New(channel string, categories []config.Category, defaultCat *config.Category) *Router {
	// Skip lines that are just the channel handle or "کانال رسمی <name>".
	pattern := fmt.Sprintf(`@%s|^%s$|کانال رسمی`, regexp.QuoteMeta(channel), regexp.QuoteMeta(channel))
	return &Router{
		channelNameRe: regexp.MustCompile("(?i)" + pattern),
		categories:    categories,
		defaultCat:    defaultCat,
	}
}

// Route classifies a single message.
func (r *Router) Route(msg eitaa.Message) Routed {
	out := Routed{Message: msg}
	out.Hashtags = extractHashtags(msg.Text)
	out.Title, out.Subtitle, out.EventDate = extractMarkers(msg.Text)
	if cat := r.pickCategory(out.Hashtags); cat != nil {
		out.Category = cat.Slug
		out.CategoryFa = cat.Label
	} else if r.defaultCat != nil {
		out.Category = r.defaultCat.Slug
		out.CategoryFa = r.defaultCat.Label
	}
	if out.Title == "" {
		out.Title = r.fallbackTitle(msg.Text)
	}
	return out
}

// RouteAll runs Route over a slice of messages.
func (r *Router) RouteAll(msgs []eitaa.Message) []Routed {
	out := make([]Routed, len(msgs))
	for i, m := range msgs {
		out[i] = r.Route(m)
	}
	return out
}

// pickCategory returns the first matching category, or nil if none match.
// Order in the config determines priority when a post carries multiple tags.
func (r *Router) pickCategory(tags []string) *config.Category {
	for _, t := range tags {
		for i := range r.categories {
			if r.categories[i].Hashtag == t {
				return &r.categories[i]
			}
		}
	}
	return nil
}

func (r *Router) fallbackTitle(text string) string {
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line == "." {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if r.channelNameRe.MatchString(line) {
			continue
		}
		return truncateRunes(line, titleMaxLen)
	}
	return ""
}

var hashtagRe = regexp.MustCompile(`#([\p{L}\p{N}_]+)`)

func extractHashtags(text string) []string {
	matches := hashtagRe.FindAllStringSubmatch(text, -1)
	tags := make([]string, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, m := range matches {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		tags = append(tags, m[1])
	}
	return tags
}

func extractMarkers(text string) (title, subtitle, eventDate string) {
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, TitleMarker) && title == "":
			title = strings.TrimSpace(strings.TrimPrefix(line, TitleMarker))
		case strings.HasPrefix(line, SubtitleMarker) && subtitle == "":
			subtitle = strings.TrimSpace(strings.TrimPrefix(line, SubtitleMarker))
		case strings.HasPrefix(line, DateMarker) && eventDate == "":
			eventDate = strings.TrimSpace(strings.TrimPrefix(line, DateMarker))
		}
	}
	return title, subtitle, eventDate
}

func truncateRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}
