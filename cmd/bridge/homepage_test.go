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

// TestProcessOne_UpdatesHomepagePoster_WhenCategoryMapped: after a successful
// publish in a category that's in POSTER_LINKS, the bridge must POST a
// link-update to the helper plugin with the right slide uid and a ?p=<id> URL.
func TestProcessOne_UpdatesHomepagePoster_WhenCategoryMapped(t *testing.T) {
	fake := newFakeHelper(t)
	r, _ := newTestRunner(t, &mockPub{nextID: 1000})
	r.cfg.WordPress.URL = fake.srv.URL
	r.cfg.Homepage.PageID = 2
	r.cfg.Homepage.PosterLinks = map[string]string{"test": "av-aaaa"}
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
	r.cfg.Homepage.PageID = 2
	r.cfg.Homepage.PosterLinks = map[string]string{"OTHER_TAG": "av-aaaa"} // doesn't include "test"
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
	r.cfg.Homepage.PageID = 2
	r.cfg.Homepage.PosterLinks = map[string]string{"test": "av-aaaa"}
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
	r.cfg.Homepage.PageID = 0
	r.cfg.Homepage.PosterLinks = map[string]string{"test": "av-aaaa"}
	// r.home stays nil (matches newRunner behaviour when PageID == 0)

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
