package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
)

// fakeEitaa serves a channel as ?before= pages; pages[0] is the first page. Counts requests per page.
type fakeEitaa struct {
	srv   *httptest.Server
	pages map[string][]int // "" for first page, "before=<id>" otherwise
	hits  map[string]int
}

func newFakeEitaa(t *testing.T, pages map[string][]int) *fakeEitaa {
	t.Helper()
	f := &fakeEitaa{pages: pages, hits: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits[r.URL.RawQuery]++
		var b strings.Builder
		for _, id := range f.pages[r.URL.RawQuery] {
			fmt.Fprintf(&b, `<div class="etme_widget_message" data-post="test/%d">`+
				`<div class="etme_widget_message_text">📌 msg %d #test</div>`+
				`<time class="time" datetime="%s"></time></div>`,
				id, id, time.Now().Add(-time.Hour).Format(time.RFC3339))
		}
		_, _ = w.Write([]byte(b.String()))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newPagedRunner(t *testing.T, f *fakeEitaa) (*runner, *recPub) {
	t.Helper()
	r, pub := newSyncRunner(t)
	r.client = eitaa.New()
	r.client.BaseURL = f.srv.URL
	r.cfg.Source.SyncWindow = 30 * 24 * time.Hour
	return r, pub
}

func seedTracked(t *testing.T, r *runner, ids ...int) {
	t.Helper()
	for _, id := range ids {
		m := eitaa.Message{ID: id, Channel: "test", Text: fmt.Sprintf("📌 msg %d #test", id), Date: time.Now().Add(-time.Hour)}
		if !r.processOne(context.Background(), m) {
			t.Fatalf("seed publish %d failed", id)
		}
	}
}

func TestExtendForSync_DeletesPostThatScrolledOffFirstPage(t *testing.T) {
	// 448 was published, then deleted on Eitaa after the first page shrank to [450, 455].
	f := newFakeEitaa(t, map[string][]int{
		"":           {450, 455},
		"before=450": {446, 449},
	})
	r, pub := newPagedRunner(t, f)
	seedTracked(t, r, 448, 450, 455)
	pub.deletes = nil

	r.tick(context.Background())

	if len(pub.deletes) != 1 || pub.deletes[0] != r.store.PostID("test", 448) {
		t.Fatalf("expected exactly the post of 448 deleted, got %v", pub.deletes)
	}
	if !r.store.Get("test", 448).Deleted {
		t.Error("448 not marked deleted in store")
	}
	if f.hits["before=450"] != 1 {
		t.Errorf("expected one ?before=450 fetch, got %d", f.hits["before=450"])
	}
}

func TestExtendForSync_NoPagingWhenFirstPageCoversWindow(t *testing.T) {
	f := newFakeEitaa(t, map[string][]int{"": {450, 455}})
	r, _ := newPagedRunner(t, f)
	seedTracked(t, r, 450, 455)

	r.tick(context.Background())

	if n := len(f.hits) - 1; n != 0 {
		t.Errorf("expected no ?before= fetches, got %v", f.hits)
	}
}

func TestExtendForSync_IgnoresTrackedPostsOutsideWindow(t *testing.T) {
	f := newFakeEitaa(t, map[string][]int{"": {450, 455}, "before=450": {446, 449}})
	r, pub := newPagedRunner(t, f)
	seedTracked(t, r, 450, 455)
	// Old post dated before the window: must not trigger paging nor deletion.
	r.store.MarkPublished("test", 300, 9999, "x", time.Now().Add(-60*24*time.Hour))
	pub.deletes = nil

	r.tick(context.Background())

	if len(pub.deletes) != 0 {
		t.Errorf("unexpected deletes: %v", pub.deletes)
	}
	if f.hits["before=450"] != 0 {
		t.Errorf("unexpected ?before= paging for out-of-window post")
	}
}

func TestExtendForSync_StopsAtChannelStart(t *testing.T) {
	// Tracked 300 is in-window but the channel has nothing before 446: walk must stop on the empty page,
	// and 300 must NOT be treated as deleted (range only extends to what was actually fetched).
	f := newFakeEitaa(t, map[string][]int{"": {450, 455}, "before=450": {446, 449}, "before=446": {}})
	r, pub := newPagedRunner(t, f)
	seedTracked(t, r, 450, 455)
	r.store.MarkPublished("test", 300, 9999, "x", time.Now().Add(-time.Hour))
	pub.deletes = nil

	r.tick(context.Background())

	if len(pub.deletes) != 0 {
		t.Errorf("unexpected deletes: %v", pub.deletes)
	}
	if f.hits["before=446"] != 1 {
		t.Errorf("expected the walk to try before=446 once, got %d", f.hits["before=446"])
	}
}
