// Package router classifies a parsed Eitaa message into a site category
// and extracts the display fields (title, subtitle, event date) that the
// channel posting guide describes.
//
// See docs/posting-guide.md for the rules this implements.
package router

import (
	"regexp"
	"strings"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
)

// Routed is an Eitaa message with site-routing metadata attached.
type Routed struct {
	eitaa.Message

	Category   string   `json:"category"`            // machine slug, e.g. "reports"
	CategoryFa string   `json:"category_fa"`         // Persian label for UI
	Title      string   `json:"title"`               // display title
	Subtitle   string   `json:"subtitle,omitempty"`  // optional subtitle
	EventDate  string   `json:"event_date,omitempty"`// Persian date string (free-form)
	Hashtags   []string `json:"hashtags"`            // every #tag found in the message
	Skip       bool     `json:"skip"`                // true → do not publish to the site
}

// Marker prefixes the channel posting guide tells admins to use.
const (
	TitleMarker    = "🔻"
	SubtitleMarker = "🟩"
	DateMarker     = "🗓"

	DefaultCategory   = "general"
	DefaultCategoryFa = "عمومی"
)

type categoryRule struct {
	Hashtag string // without the leading #
	Slug    string
	Label   string
}

// defaultRules maps a category hashtag to a site category.
// First match wins — order matters when a post carries multiple tags.
var defaultRules = []categoryRule{
	{"گزارش_تصویری", "reports", "گزارش‌ها"},
	{"اطلاعیه", "announcements", "اطلاعیه‌ها"},
	{"مناسبت", "occasions", "مناسبت‌ها"},
	{"کمک_مالی", "support", "حمایت"},
	{"خبر", "news", "اخبار"},
	{"معرفی", "about", "معرفی"},
	{"پاسخ", "faq", "سوالات متداول"},
}

// skipHashtags marks posts that must never be published to the site.
var skipHashtags = map[string]bool{
	"خصوصی":     true,
	"نمایش_نده": true,
}

// hashtag matches Persian/Latin word chars and underscores after a #.
var hashtagRe = regexp.MustCompile(`#([\p{L}\p{N}_]+)`)

// channelNameRe matches lines that are just the institute self-reference,
// so they are not mistaken for the post title.
var channelNameRe = regexp.MustCompile(`@Merajyan|موسسه_معراج|کانال رسمی`)

// Route classifies a single message.
func Route(msg eitaa.Message) Routed {
	r := Routed{Message: msg, Category: DefaultCategory, CategoryFa: DefaultCategoryFa}
	r.Hashtags = extractHashtags(msg.Text)
	r.Skip = anyMatch(r.Hashtags, skipHashtags)
	r.Category, r.CategoryFa = pickCategory(r.Hashtags)
	r.Title, r.Subtitle, r.EventDate = extractMarkers(msg.Text)
	if r.Title == "" {
		r.Title = fallbackTitle(msg.Text)
	}
	return r
}

// RouteAll runs Route over a slice of messages.
func RouteAll(msgs []eitaa.Message) []Routed {
	out := make([]Routed, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, Route(m))
	}
	return out
}

func extractHashtags(text string) []string {
	matches := hashtagRe.FindAllStringSubmatch(text, -1)
	seen := map[string]bool{}
	var tags []string
	for _, m := range matches {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		tags = append(tags, m[1])
	}
	return tags
}

func anyMatch(tags []string, set map[string]bool) bool {
	for _, t := range tags {
		if set[t] {
			return true
		}
	}
	return false
}

func pickCategory(tags []string) (string, string) {
	for _, t := range tags {
		for _, rule := range defaultRules {
			if rule.Hashtag == t {
				return rule.Slug, rule.Label
			}
		}
	}
	return DefaultCategory, DefaultCategoryFa
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

func fallbackTitle(text string) string {
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line == "." {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if channelNameRe.MatchString(line) {
			continue
		}
		return truncateRunes(line, 80)
	}
	return ""
}

func truncateRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}

// CategoryLabels returns the user-facing Persian label for every known
// category. Useful for building UI filter tabs.
func CategoryLabels() map[string]string {
	out := map[string]string{DefaultCategory: DefaultCategoryFa}
	for _, r := range defaultRules {
		out[r.Slug] = r.Label
	}
	return out
}
