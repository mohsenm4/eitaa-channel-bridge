package publisher

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestEitaaPhotoHash_StableAcrossRotatingTokens(t *testing.T) {
	const want = "ad20d10ca1e8ae253c2067b1c6ba371a"
	urls := []string{
		"https://eitaa.com/download_ad20d10ca1e8ae253c2067b1c6ba371a?token=78da01a4xxxxx",
		"https://eitaa.com/download_ad20d10ca1e8ae253c2067b1c6ba371a?token=DIFFERENT_TOKEN",
		"https://eitaa.com/download_ad20d10ca1e8ae253c2067b1c6ba371a",
	}
	for _, u := range urls {
		if got := eitaaPhotoHash(u); got != want {
			t.Errorf("eitaaPhotoHash(%q) = %q, want %q", u, got, want)
		}
	}
	if got := eitaaPhotoHash("https://example.com/no-marker.jpg"); got != "" {
		t.Errorf("eitaaPhotoHash(no marker) = %q, want empty", got)
	}
}

func TestPhotoFilename_SameHashSameNameAcrossMessages(t *testing.T) {
	url16 := "https://eitaa.com/download_51d92d48378733c32acf9c43adb1e5fb?token=AAA"
	url20 := "https://eitaa.com/download_51d92d48378733c32acf9c43adb1e5fb?token=BBB"
	if photoFilename(url16, 16) != photoFilename(url20, 20) {
		t.Errorf("filenames should match for same photo hash:\n  %s\n  %s",
			photoFilename(url16, 16), photoFilename(url20, 20))
	}
	if !strings.Contains(photoFilename(url16, 16), "51d92d48378733c32acf9c43adb1e5fb") {
		t.Errorf("filename should embed hash: %s", photoFilename(url16, 16))
	}
}

func TestPhotoFilename_FallbackWhenNoHash(t *testing.T) {
	got := photoFilename("https://example.com/strange.png", 42)
	if !strings.Contains(got, "fallback-42") {
		t.Errorf("expected fallback filename for unmarked URL, got %q", got)
	}
}

func TestMergeOrInsertGallery_InsertsBeforeClosingRow(t *testing.T) {
	content := `[vc_row][vc_column][vc_column_text]body text[/vc_column_text][/vc_column][/vc_row]`
	got := mergeOrInsertGallery(content, []int{111, 222})

	if !strings.Contains(got, `[vc_gallery interval="3" images="111,222"`) {
		t.Errorf("expected gallery with new ids, got:\n%s", got)
	}
	// Gallery must be inside the body row, not in a new one.
	if strings.Count(got, "[vc_row]") != 1 {
		t.Errorf("expected exactly 1 [vc_row], got:\n%s", got)
	}
	// Body text must still come before the gallery.
	if strings.Index(got, "body text") > strings.Index(got, "[vc_gallery") {
		t.Errorf("gallery should come after body text, got:\n%s", got)
	}
}

func TestMergeOrInsertGallery_MergesIntoExistingGallery(t *testing.T) {
	content := `[vc_row][vc_column][vc_column_text]b[/vc_column_text][vc_gallery interval="3" images="10,20,30" img_size="full" onclick=""][/vc_column][/vc_row]`
	got := mergeOrInsertGallery(content, []int{40, 50})

	// Original ids preserved, new ids appended, no duplicate gallery.
	if !strings.Contains(got, `images="10,20,30,40,50"`) {
		t.Errorf("expected merged ids, got:\n%s", got)
	}
	if strings.Count(got, "vc_gallery") != 1 {
		t.Errorf("expected exactly 1 vc_gallery (merge, not append), got:\n%s", got)
	}
}

func TestMergeOrInsertGallery_DedupesAcrossExistingAndNew(t *testing.T) {
	content := `[vc_gallery images="10,20,30"]`
	got := mergeOrInsertGallery(content, []int{20, 40, 30, 50})

	if !strings.Contains(got, `images="10,20,30,40,50"`) {
		t.Errorf("expected dedup-preserved ids, got:\n%s", got)
	}
}

func TestMergeOrInsertGallery_EmptyInputReturnsContentUnchanged(t *testing.T) {
	content := `[vc_row][vc_column]existing[/vc_column][/vc_row]`
	if got := mergeOrInsertGallery(content, nil); got != content {
		t.Errorf("empty ids should be no-op, got %q", got)
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

// TestPublish_AdoptsExistingPostWhenSlugAlreadyOnSite reproduces the lost-response
// scenario from issue #23: a prior publish attempt created a post on WP, but its
// HTTP response never reached us. On retry, Publish must find the existing post by
// slug and return its ID instead of POSTing a second copy.
func TestPublish_AdoptsExistingPostWhenSlugAlreadyOnSite(t *testing.T) {
	const existingPostID = 20001
	var postCreates atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/wp-json/wp/v2/posts") &&
			r.URL.Query().Get("slug") != "":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":20001}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/wp-json/wp/v2/posts":
			postCreates.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":99999,"link":"http://example.com/?p=99999"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	wp := NewWordPress(config.WordPressTarget{
		URL: srv.URL, Username: "u", AppPassword: "p", Status: "publish",
	}, slog.Default())

	got, err := wp.Publish(context.Background(), testRouted())
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got != existingPostID {
		t.Errorf("Publish returned %d, want %d (adopted existing post)", got, existingPostID)
	}
	if n := postCreates.Load(); n != 0 {
		t.Errorf("expected zero POST /posts calls when adopting, got %d", n)
	}
}

// TestPublish_ProceedsToInsertWhenSlugIsFree confirms the happy path: a fresh slug
// returns an empty array from the lookup, and Publish proceeds to actually create
// the post.
func TestPublish_ProceedsToInsertWhenSlugIsFree(t *testing.T) {
	var postCreates atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/wp-json/wp/v2/posts") &&
			r.URL.Query().Get("slug") != "":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && r.URL.Path == "/wp-json/wp/v2/categories":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":7,"name":"واحد قرض الحسنه","slug":"qarz-al-hasaneh"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/wp-json/eitaa-bridge/v1":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/wp-json/wp/v2/posts"):
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/wp-json/wp/v2/posts":
			postCreates.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":12345,"link":"http://example.com/?p=12345"}`))
		default:
			t.Logf("unexpected request: %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	wp := NewWordPress(config.WordPressTarget{
		URL: srv.URL, Username: "u", AppPassword: "p", Status: "publish",
	}, slog.Default())

	got, err := wp.Publish(context.Background(), testRouted())
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got != 12345 {
		t.Errorf("Publish returned %d, want 12345 (newly-created post)", got)
	}
	if n := postCreates.Load(); n != 1 {
		t.Errorf("expected exactly 1 POST /posts call, got %d", n)
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
