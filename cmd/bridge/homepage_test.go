package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

// fakeHelperServer captures one /update-slide-link call so tests can assert
// the parameters sent by the bridge after a publish.
type fakeHelperServer struct {
	srv        *httptest.Server
	hits       atomic.Int32
	gotPageID  int
	gotUID     string
	gotLink    string
	statusCode int
}

func newFakeHelper(t *testing.T) *fakeHelperServer {
	t.Helper()
	f := &fakeHelperServer{statusCode: 200}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/update-slide-link") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			PageID int    `json:"page_id"`
			UID    string `json:"uid"`
			Link   string `json:"link"`
		}
		_ = json.Unmarshal(body, &payload)
		f.gotPageID = payload.PageID
		f.gotUID = payload.UID
		f.gotLink = payload.Link
		f.hits.Add(1)
		w.WriteHeader(f.statusCode)
		w.Write([]byte(`{"page_id":` + itoa(payload.PageID) + `,"touched":["_aviaLayoutBuilderCleanData","post_content"]}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

type fakeHelperServerForNews struct {
	srv             *httptest.Server
	hits            atomic.Int32
	gotPageID       int
	gotCategorySlug string
	statusCode      int
}

func newFakeHelperForNews(t *testing.T) *fakeHelperServerForNews {
	t.Helper()
	f := &fakeHelperServerForNews{statusCode: 200}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/reconcile-news-section"):
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				PageID        int    `json:"page_id"`
				CategorySlug  string `json:"category_slug"`
			}
			_ = json.Unmarshal(body, &payload)
			f.gotPageID = payload.PageID
			f.gotCategorySlug = payload.CategorySlug
			f.hits.Add(1)
			w.WriteHeader(f.statusCode)
			w.Write([]byte(`{"reconciled":true,"card_count":3,"touched":["_aviaLayoutBuilderCleanData","post_content"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestProcessOne_ReconcilesNewsSection_OnAkhbarPublish(t *testing.T) {
	fake := newFakeHelperForNews(t)
	r, _ := newTestRunner(t, &mockPub{nextID: 1000})
	r.cfg.Publishing.Categories[0].Slug = newsCategorySlug
	r.cfg.Publishing.Categories[0].Label = "اخبار اطلاعیه"
	r.rt = router.New("test", r.cfg.Publishing.Categories, nil)
	r.cfg.WordPress.URL = fake.srv.URL
	r.homePageID = 2
	r.home = newHomepageClient(fake.srv.URL, "u", "p")

	msg := eitaa.Message{
		ID: 50, Channel: "test",
		Text: "📌 a news item\n\n#test",
		Date: time.Now(),
	}
	if !r.processOne(context.Background(), msg) {
		t.Fatal("processOne returned false")
	}
	if got := fake.hits.Load(); got != 1 {
		t.Fatalf("expected 1 reconcile-news-section call, got %d", got)
	}
	if fake.gotPageID != 2 {
		t.Errorf("page_id = %d, want 2", fake.gotPageID)
	}
	if fake.gotCategorySlug != "اخبار اطلاعیه" {
		t.Errorf("category_slug = %q, want %q", fake.gotCategorySlug, "اخبار اطلاعیه")
	}
}

func TestProcessOne_SkipsNewsReconcile_WhenCategoryDifferent(t *testing.T) {
	fake := newFakeHelperForNews(t)
	r, _ := newTestRunner(t, &mockPub{nextID: 1000})
	// Default test slug is "test", not the news slug.
	r.cfg.WordPress.URL = fake.srv.URL
	r.homePageID = 2
	r.home = newHomepageClient(fake.srv.URL, "u", "p")

	msg := eitaa.Message{
		ID: 51, Channel: "test",
		Text: "📌 a non-news report\n\n#test",
		Date: time.Now(),
	}
	if !r.processOne(context.Background(), msg) {
		t.Fatal("processOne returned false")
	}
	if got := fake.hits.Load(); got != 0 {
		t.Errorf("expected zero reconcile-news-section calls, got %d", got)
	}
}

func TestProcessOne_NewsReconcileFailure_DoesNotFailPublish(t *testing.T) {
	fake := newFakeHelperForNews(t)
	fake.statusCode = 500
	r, seenPath := newTestRunner(t, &mockPub{nextID: 1000})
	r.cfg.Publishing.Categories[0].Slug = newsCategorySlug
	r.cfg.Publishing.Categories[0].Label = "اخبار اطلاعیه"
	r.rt = router.New("test", r.cfg.Publishing.Categories, nil)
	r.cfg.WordPress.URL = fake.srv.URL
	r.homePageID = 2
	r.home = newHomepageClient(fake.srv.URL, "u", "p")

	msg := eitaa.Message{
		ID: 52, Channel: "test",
		Text: "📌 a news item\n\n#test",
		Date: time.Now(),
	}
	if !r.processOne(context.Background(), msg) {
		t.Fatal("processOne returned false despite publish succeeding")
	}
	if !reloadSeen(t, seenPath).Seen("test", 52) {
		t.Error("publish marked unseen due to homepage reconcile failure — would cause duplicate next tick")
	}
}

// TestProcessOne_UpdatesHomepagePoster_WhenCategoryMapped: after a successful
// publish in a category that's in POSTER_LINKS, the bridge must POST a
// link-update to the helper plugin with the right slide uid and a ?p=<id> URL.
func TestProcessOne_UpdatesHomepagePoster_WhenCategoryMapped(t *testing.T) {
	fake := newFakeHelper(t)
	r, _ := newTestRunner(t, &mockPub{nextID: 1000})
	r.cfg.WordPress.URL = fake.srv.URL
	r.homePageID = 2
	r.posterLinks = map[string]string{"test": "av-aaaa"}
	r.home = newHomepageClient(fake.srv.URL, "u", "p")

	msg := eitaa.Message{
		ID: 42, Channel: "test",
		Text: "📌 a report\n\n#test",
		Date: time.Now(),
	}
	if !r.processOne(context.Background(), msg) {
		t.Fatal("processOne returned false")
	}
	if got := fake.hits.Load(); got != 1 {
		t.Fatalf("expected 1 update-slide-link call, got %d", got)
	}
	if fake.gotPageID != 2 {
		t.Errorf("page_id = %d, want 2", fake.gotPageID)
	}
	if fake.gotUID != "av-aaaa" {
		t.Errorf("uid = %q, want %q", fake.gotUID, "av-aaaa")
	}
	wantLinkSuffix := "/?p=1001"
	if !strings.HasSuffix(fake.gotLink, wantLinkSuffix) {
		t.Errorf("link = %q, want suffix %q", fake.gotLink, wantLinkSuffix)
	}
}

// TestProcessOne_SkipsHomepageUpdate_WhenCategoryNotMapped: a publish in a
// category that has no entry in POSTER_LINKS must NOT call the helper plugin.
func TestProcessOne_SkipsHomepageUpdate_WhenCategoryNotMapped(t *testing.T) {
	fake := newFakeHelper(t)
	r, _ := newTestRunner(t, &mockPub{nextID: 1000})
	r.cfg.WordPress.URL = fake.srv.URL
	r.homePageID = 2
	r.posterLinks = map[string]string{"OTHER_TAG": "av-aaaa"} // doesn't include "test"
	r.home = newHomepageClient(fake.srv.URL, "u", "p")

	msg := eitaa.Message{
		ID: 43, Channel: "test",
		Text: "📌 a report\n\n#test",
		Date: time.Now(),
	}
	if !r.processOne(context.Background(), msg) {
		t.Fatal("processOne returned false")
	}
	if got := fake.hits.Load(); got != 0 {
		t.Errorf("expected zero update-slide-link calls, got %d", got)
	}
}

// TestProcessOne_HomepageFailure_DoesNotFailPublish: the post is already on
// WP by the time we update the homepage; a non-2xx from the helper plugin
// must be logged but must NOT cause processOne to return false (otherwise
// the message would be re-processed next tick → duplicate post).
func TestProcessOne_HomepageFailure_DoesNotFailPublish(t *testing.T) {
	fake := newFakeHelper(t)
	fake.statusCode = 500
	r, seenPath := newTestRunner(t, &mockPub{nextID: 1000})
	r.cfg.WordPress.URL = fake.srv.URL
	r.homePageID = 2
	r.posterLinks = map[string]string{"test": "av-aaaa"}
	r.home = newHomepageClient(fake.srv.URL, "u", "p")

	msg := eitaa.Message{
		ID: 44, Channel: "test",
		Text: "📌 a report\n\n#test",
		Date: time.Now(),
	}
	if !r.processOne(context.Background(), msg) {
		t.Fatal("processOne returned false despite the publish succeeding")
	}
	if !reloadSeen(t, seenPath).Seen("test", 44) {
		t.Error("publish marked unseen due to homepage failure — would cause duplicate next tick")
	}
}

// TestProcessOne_NoHomepageClient_NoCalls: when Homepage.PageID is 0 the
// runner has no homepage client; no requests must be made even if a mapping
// happens to be configured.
func TestProcessOne_NoHomepageClient_NoCalls(t *testing.T) {
	fake := newFakeHelper(t)
	r, _ := newTestRunner(t, &mockPub{nextID: 1000})
	r.homePageID = 0
	r.posterLinks = map[string]string{"test": "av-aaaa"}
	// r.home stays nil (matches newRunner behaviour before discovery)

	msg := eitaa.Message{
		ID: 45, Channel: "test",
		Text: "📌 a report\n\n#test",
		Date: time.Now(),
	}
	if !r.processOne(context.Background(), msg) {
		t.Fatal("processOne returned false")
	}
	if got := fake.hits.Load(); got != 0 {
		t.Errorf("expected zero calls, got %d", got)
	}
}
