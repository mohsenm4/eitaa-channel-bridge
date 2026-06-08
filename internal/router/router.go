// Package router classifies a parsed Eitaa message and extracts title / date / hashtags.
package router

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
)

type Routed struct {
	eitaa.Message

	Category   string   `json:"category,omitempty"`
	CategoryFa string   `json:"category_fa,omitempty"`
	Title      string   `json:"title"`
	Subtitle   string   `json:"subtitle,omitempty"`
	EventDate  string   `json:"event_date,omitempty"`
	Hashtags   []string `json:"hashtags"`

	// Warnings carry format issues the channel author should fix; runner writes them to data/warnings.log.
	Warnings []string `json:"warnings,omitempty"`
}

// Line-prefix markers the channel posting guide tells admins to use.
const (
	TitleMarker    = "📌"
	DateMarker     = "📅"
	BodyMarker     = "📝"
	SubtitleMarker = "🟩" // legacy; kept so old messages still parse.

	titleMaxLen = 80
)

type Router struct {
	channelNameRe *regexp.Regexp
	categories    []config.Category
	defaultCat    *config.Category
}

func New(channel string, categories []config.Category, defaultCat *config.Category) *Router {
	pattern := fmt.Sprintf(`@%s|^%s$|کانال رسمی`, regexp.QuoteMeta(channel), regexp.QuoteMeta(channel))
	return &Router{
		channelNameRe: regexp.MustCompile("(?i)" + pattern),
		categories:    categories,
		defaultCat:    defaultCat,
	}
}

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
		if out.Title != "" {
			out.Warnings = append(out.Warnings,
				"بدون "+TitleMarker+" — خط اول به‌عنوان عنوان گذاشته شد")
		}
	}
	if out.EventDate == "" {
		out.Warnings = append(out.Warnings, "بدون "+DateMarker+" — تاریخ تنظیم نشد")
	}
	return out
}

func (r *Router) RouteAll(msgs []eitaa.Message) []Routed {
	out := make([]Routed, len(msgs))
	for i, m := range msgs {
		out[i] = r.Route(m)
	}
	return out
}

// pickCategory returns the first match; config order is the tie-breaker.
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
