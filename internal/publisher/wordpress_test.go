package publisher

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

func testWP() *WordPress {
	return NewWordPress(config.WordPressTarget{URL: "http://example.com"}, slog.Default())
}

func testRouted() router.Routed {
	return router.Routed{
		Message: eitaa.Message{
			Channel: "tesssst",
			ID:      42,
			Text:    "📌 گزارش\n📅 1404/06/31\n📝\nخط ۱\nخط ۲\n#قرض_الحسنه",
		},
		CategoryFa: "واحد قرض الحسنه",
		Title:      "گزارش",
		EventDate:  "1404/06/31",
		Hashtags:   []string{"قرض_الحسنه"},
	}
}

func TestApplyTemplate_KeepsFirstColumnTextAndSwapsImage(t *testing.T) {
	template := `[vc_row][vc_column][vc_single_image image="18403" img_size="full" alignment="center"][vc_column_text]
قال امیر المومنین علی علیه السلام:
وَ اغْتَنِمْ مَنِ اسْتَقْرَضَكَ ...
[/vc_column_text][vc_column_text]
پاییز 1403 – گزارش ماهانه واحد قرض الحسنه ...
[/vc_column_text][/vc_column][/vc_row]`

	got, ok := testWP().applyTemplate(template, testRouted(), 99999)
	if !ok {
		t.Fatal("applyTemplate returned !ok on a well-formed template")
	}
	// First [vc_column_text] (hadith) must be preserved verbatim
	if !strings.Contains(got, "قال امیر المومنین علی علیه السلام") {
		t.Error("hadith block was lost")
	}
	// New body must be present
	if !strings.Contains(got, "خط ۱") || !strings.Contains(got, "خط ۲") {
		t.Error("new body lines missing")
	}
	// Featured image must use the NEW media id, not the template's
	if !strings.Contains(got, `image="99999"`) {
		t.Error("new featured image id missing")
	}
	if strings.Contains(got, `image="18403"`) {
		t.Error("template's old image id should not appear")
	}
	// Per-report stats (پاییز 1403...) from old post must NOT carry over
	if strings.Contains(got, "پاییز 1403") {
		t.Error("per-report stats from template should be dropped")
	}
	// Marker lines must not appear in body
	if strings.Contains(got, "📌") {
		t.Error("title marker leaked into body")
	}
}

func TestApplyTemplate_FallbackWhenNoColumnText(t *testing.T) {
	garbage := `<p>just html, no shortcodes</p>`
	if _, ok := testWP().applyTemplate(garbage, testRouted(), 0); ok {
		t.Error("expected !ok for template without [vc_column_text]")
	}
}

func TestApplyTemplate_NoFeaturedImage(t *testing.T) {
	template := `[vc_column_text]حدیث[/vc_column_text]`
	got, ok := testWP().applyTemplate(template, testRouted(), 0)
	if !ok {
		t.Fatal("applyTemplate failed unexpectedly")
	}
	if strings.Contains(got, "vc_single_image") {
		t.Error("vc_single_image should be skipped when featuredID is 0")
	}
}

func TestRenderHTML_FallbackWrap(t *testing.T) {
	got := testWP().renderHTML(testRouted())
	if !strings.Contains(got, "[vc_row][vc_column][vc_column_text]") {
		t.Error("fallback should wrap in WPBakery row/column/column_text")
	}
	if !strings.Contains(got, "خط ۱") {
		t.Error("body line missing in fallback")
	}
}

// TestApplyTemplate_SingleBlockClonesStructure covers the "روضه خانگی"
// style template — a single [vc_column_text] block. The whole outer
// structure must be preserved and only the body content replaced.
func TestApplyTemplate_SingleBlockClonesStructure(t *testing.T) {
	template := `[vc_row][vc_column][vc_column_text]
طرح فرهنگی روضه خانگی...
[/vc_column_text][/vc_column][/vc_row]`

	got, ok := testWP().applyTemplate(template, testRouted(), 0)
	if !ok {
		t.Fatal("applyTemplate failed on single-block template")
	}
	if !strings.HasPrefix(got, "[vc_row][vc_column][vc_column_text]") {
		t.Errorf("outer structure not preserved: %q", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "[/vc_column_text][/vc_column][/vc_row]") {
		t.Errorf("outer structure not preserved (suffix): %q", got)
	}
	if strings.Contains(got, "روضه خانگی") {
		t.Error("old body should be replaced, not kept")
	}
	if !strings.Contains(got, "خط ۱") {
		t.Error("new body missing")
	}
}

// TestApplyTemplate_PreservesExtraShortcodes ensures dividers, separators,
// and additional rows between vc_column_text blocks survive the clone.
func TestApplyTemplate_PreservesExtraShortcodes(t *testing.T) {
	template := `[vc_row][vc_column][vc_column_text]حدیث[/vc_column_text][vc_separator][vc_column_text]old body[/vc_column_text][/vc_column][/vc_row]`

	got, ok := testWP().applyTemplate(template, testRouted(), 0)
	if !ok {
		t.Fatal("applyTemplate failed")
	}
	if !strings.Contains(got, "[vc_separator]") {
		t.Error("vc_separator must be preserved verbatim")
	}
	if !strings.Contains(got, "حدیث") {
		t.Error("first block (hadith) must be preserved")
	}
	if strings.Contains(got, "old body") {
		t.Error("last block content should be replaced")
	}
}

// TestApplyTemplate_SwapsOnlyFirstImage guards against accidentally
// rewriting every vc_single_image when the template has more than one.
func TestApplyTemplate_SwapsOnlyFirstImage(t *testing.T) {
	template := `[vc_single_image image="111" img_size="full"][vc_column_text]a[/vc_column_text][vc_single_image image="222" img_size="full"][vc_column_text]b[/vc_column_text]`

	got, ok := testWP().applyTemplate(template, testRouted(), 99999)
	if !ok {
		t.Fatal("applyTemplate failed")
	}
	if !strings.Contains(got, `image="99999"`) {
		t.Error("first image id should be swapped to the new featured id")
	}
	if !strings.Contains(got, `image="222"`) {
		t.Error("subsequent image ids must not be touched")
	}
	if strings.Contains(got, `image="111"`) {
		t.Error("old first image id should be gone")
	}
}
