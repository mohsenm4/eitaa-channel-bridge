package publisher

import (
	"embed"
	"strings"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// loadCategoryTemplate returns the raw template text for a category
// slug, or ("", false) if no template file is bundled for it. The
// publisher falls back to the in-place "preserve & swap" logic when
// false is returned, so adding a category is just dropping a .tmpl in
// templates/ — no code change.
func loadCategoryTemplate(slug string) (string, bool) {
	data, err := templateFS.ReadFile("templates/" + slug + ".tmpl")
	if err != nil {
		return "", false
	}
	return string(data), true
}

// renderCategoryTemplate fills {{KEY}} placeholders in tmpl using vars
// (string substitution; templates are trusted, so no html/template).
// Unknown keys are left as-is so missing fields surface as visible
// {{...}} placeholders in the draft — easier to spot in review than a
// silently empty slot.
func renderCategoryTemplate(tmpl string, vars map[string]string) string {
	out := tmpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

// extractCategoryVars produces the placeholder map for a category. Each
// category has its own parser keyed off the slug. Returning nil means
// "no parser for this category — let the caller fall back".
func extractCategoryVars(msg router.Routed, featuredID int) map[string]string {
	switch msg.Category {
	case "qarz-al-hasaneh", "tavanmandsazi", "rezvan":
		return parseSimpleReport(msg, featuredID)
	}
	return nil
}

// parseSimpleReport is the no-table parser for narrative-driven
// categories. Featured image isn't injected into content (the theme
// renders it from featured_media); we only fill body and the centered
// title repeat at the bottom — the pattern used by existing
// توانمندسازی posts.
func parseSimpleReport(msg router.Routed, _ int) map[string]string {
	body := strings.TrimSpace(stripMarkersAndHashtags(msg.Text))
	return map[string]string{
		"TITLE":      msg.Title,
		"EVENT_DATE": msg.EventDate,
		"BODY":       body,
	}
}

// stripMarkersAndHashtags removes lines that start with a router marker
// (📌 / 📅 / 📝 / legacy 🟩) and lines that are only hashtags, leaving
// the substantive body for regex matching.
func stripMarkersAndHashtags(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			b.WriteByte('\n')
			continue
		}
		if hasAnyPrefix(t, router.TitleMarker, router.DateMarker,
			router.BodyMarker, router.SubtitleMarker) {
			continue
		}
		if isHashtagOnlyLine(t) {
			continue
		}
		b.WriteString(t)
		b.WriteByte('\n')
	}
	return b.String()
}
