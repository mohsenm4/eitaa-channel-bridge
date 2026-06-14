package publisher

import (
	"embed"
	"strings"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// loadCategoryTemplate returns the raw template text for a slug, or ("", false) if no .tmpl is bundled.
func loadCategoryTemplate(slug string) (string, bool) {
	data, err := templateFS.ReadFile("templates/" + slug + ".tmpl")
	if err != nil {
		return "", false
	}
	return string(data), true
}

// renderCategoryTemplate fills {{KEY}} placeholders by string substitution; unknown keys stay visible.
func renderCategoryTemplate(tmpl string, vars map[string]string) string {
	out := tmpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

// extractCategoryVars returns the placeholder map for a category, or nil to skip templating.
func extractCategoryVars(msg router.Routed, featuredID int) map[string]string {
	switch msg.Category {
	case "qarz-al-hasaneh", "tavanmandsazi", "rezvan", "hemayat-khedmat":
		return parseSimpleReport(msg, featuredID)
	}
	return nil
}

// parseSimpleReport is the narrative-style parser: title + date + body (featured image set via featured_media).
func parseSimpleReport(msg router.Routed, _ int) map[string]string {
	body := strings.TrimSpace(stripMarkersAndHashtags(msg.Text))
	return map[string]string{
		"TITLE":      msg.Title,
		"EVENT_DATE": msg.EventDate,
		"BODY":       body,
	}
}

// stripMarkersAndHashtags drops 📌/📅/📝/🟩 lines and hashtag-only lines from the message text.
func stripMarkersAndHashtags(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			b.WriteByte('\n')
			continue
		}
		if hasAnyPrefix(t, router.TitleMarker, router.DateMarker, router.BodyMarker, router.SubtitleMarker) {
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
