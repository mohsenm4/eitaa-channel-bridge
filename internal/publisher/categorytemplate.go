package publisher

import (
	"embed"
	"fmt"
	"regexp"
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
	case "qarz-al-hasaneh":
		return parseQarzReport(msg, featuredID)
	case "tavanmandsazi":
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

// --- per-category parsers ---

var (
	qarzPeriodRe    = regexp.MustCompile(`(?m)^(.+?از تاریخ\s*\S+\s*تا\s*\S+)\s*$`)
	qarzLoanCountRe = regexp.MustCompile(`تعداد وام\s*های تایید شده[:：\s]*([0-9۰-۹,٬]+)`)
	qarzTotalRe     = regexp.MustCompile(`مبلغ کل وام\s*های اعطایی[:：\s]*([0-9۰-۹,٬]+)`)
	qarzBreakdownRe = regexp.MustCompile(`(?m)^[-–•]\s*(.+?)[:：]\s*([0-9۰-۹,٬]+)\s*$`)
	qarzSummaryRe   = regexp.MustCompile(`(?m)^(مجموع مبلغ.+)$`)
)

func parseQarzReport(msg router.Routed, featuredID int) map[string]string {
	body := stripMarkersAndHashtags(msg.Text)
	vars := map[string]string{
		"FEATURED_ID":        fmt.Sprintf("%d", featuredID),
		"EVENT_DATE":         msg.EventDate,
		"PERIOD_DESCRIPTION": firstSubmatch(qarzPeriodRe, body),
		"LOAN_COUNT":         firstSubmatch(qarzLoanCountRe, body),
		"TOTAL_AMOUNT":       firstSubmatch(qarzTotalRe, body),
		"SUMMARY":            firstSubmatch(qarzSummaryRe, body),
		"BREAKDOWN_LIST":     renderBreakdown(qarzBreakdownRe.FindAllStringSubmatch(body, -1)),
	}
	return vars
}

func renderBreakdown(matches [][]string) string {
	var b strings.Builder
	for _, m := range matches {
		fmt.Fprintf(&b, "– %s: <strong>%s</strong>\n", strings.TrimSpace(m[1]), strings.TrimSpace(m[2]))
	}
	return strings.TrimRight(b.String(), "\n")
}

func firstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
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
