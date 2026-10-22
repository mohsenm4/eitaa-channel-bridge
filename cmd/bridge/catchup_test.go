package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// failPagedFetch lets the first page through but fails every ?before= request, like a flaky Eitaa.
type failPagedFetch struct{ next http.RoundTripper }

func (t failPagedFetch) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.RawQuery != "" {
		return nil, errors.New("simulated network error")
	}
	return t.next.RoundTrip(req)
}

func TestCatchUp_PublishesBurstBeyondFirstPage(t *testing.T) {
	// Last seen is 10; six new messages arrived but the first page only shows the newest three.
	f := newFakeEitaa(t, map[string][]int{
		"":          {14, 15, 16},
		"before=14": {11, 12, 13},
	})
	r, _ := newPagedRunner(t, f)
	r.cfg.Source.SyncWindow = 0
	seedTracked(t, r, 10)

	r.tick(context.Background())

	prev := r.store.PostID("test", 10)
	for id := 11; id <= 16; id++ {
		post := r.store.PostID("test", id)
		if post == 0 {
			t.Fatalf("message %d was not published", id)
		}
		if post <= prev {
			t.Errorf("message %d published out of order (post %d after %d)", id, post, prev)
		}
		prev = post
	}
	if f.hits["before=14"] != 1 || f.hits["before=11"] != 0 {
		t.Errorf("expected exactly one catch-up page, got hits %v", f.hits)
	}
}

func TestCatchUp_NoExtraFetchWhenFirstPageReachesLastSeen(t *testing.T) {
	// Normal tick: the first page already contains seen messages, so nothing changes from before.
	f := newFakeEitaa(t, map[string][]int{"": {450, 455, 456}})
	r, _ := newPagedRunner(t, f)
	r.cfg.Source.SyncWindow = 0
	seedTracked(t, r, 450, 455)

	r.tick(context.Background())

	if len(f.hits) != 1 {
		t.Errorf("expected only the first-page fetch, got %v", f.hits)
	}
	if r.store.PostID("test", 456) == 0 {
		t.Error("456 was not published")
	}
}

func TestCatchUp_FreshInstallKeepsFirstPageOnly(t *testing.T) {
	// Empty store: same as before this change — publish the first page, never walk into history.
	f := newFakeEitaa(t, map[string][]int{"": {5, 6}, "before=5": {1, 2, 3, 4}})
	r, _ := newPagedRunner(t, f)
	r.cfg.Source.SyncWindow = 0

	r.tick(context.Background())

	if f.hits["before=5"] != 0 {
		t.Errorf("fresh install must not page back, got hits %v", f.hits)
	}
	for _, id := range []int{1, 2, 3, 4} {
		if r.store.Seen("test", id) {
			t.Errorf("history message %d must be left alone", id)
		}
	}
	if r.store.PostID("test", 5) == 0 || r.store.PostID("test", 6) == 0 {
		t.Error("first page was not published")
	}
}

func TestCatchUp_SkipsUnseenMessagesOlderThanLastSeen(t *testing.T) {
	// The catch-up page reaches past last seen (10) into history that was never published (8, 9).
	f := newFakeEitaa(t, map[string][]int{"": {14, 15}, "before=14": {8, 9, 10, 11, 12, 13}})
	r, _ := newPagedRunner(t, f)
	r.cfg.Source.SyncWindow = 0
	seedTracked(t, r, 10)

	r.tick(context.Background())

	for _, id := range []int{8, 9} {
		if r.store.Seen("test", id) {
			t.Errorf("message %d is older than last seen and must not be published", id)
		}
	}
	for _, id := range []int{11, 12, 13, 14, 15} {
		if r.store.PostID("test", id) == 0 {
			t.Errorf("message %d was not published", id)
		}
	}
}

func TestCatchUp_StopsAtPageCapAndWarns(t *testing.T) {
	// One message per page and last seen far behind: the walk must stop after catchUpMaxPages.
	pages := map[string][]int{"": {100}}
	for id := 100; id > 80; id-- {
		pages[fmt.Sprintf("before=%d", id)] = []int{id - 1}
	}
	f := newFakeEitaa(t, pages)
	r, _ := newPagedRunner(t, f)
	r.cfg.Source.SyncWindow = 0
	seedTracked(t, r, 50)
	logs := captureLogs(r)

	r.tick(context.Background())

	if got := len(f.hits) - 1; got != catchUpMaxPages {
		t.Errorf("expected %d catch-up pages, got %d", catchUpMaxPages, got)
	}
	if !strings.Contains(logs.String(), "catch-up hit its page cap") {
		t.Errorf("expected a page-cap warning, got logs: %s", logs.String())
	}
	if r.store.PostID("test", 90) == 0 || r.store.Seen("test", 89) {
		t.Error("expected 90..100 published and nothing older touched")
	}
}

func TestCatchUp_FetchErrorStillPublishesFirstPage(t *testing.T) {
	// Known trade-off: a failed catch-up page loses the gap (11..13) like before, but never blocks 14..16.
	f := newFakeEitaa(t, map[string][]int{"": {14, 15, 16}, "before=14": {11, 12, 13}})
	r, _ := newPagedRunner(t, f)
	r.cfg.Source.SyncWindow = 0
	r.client.HTTPClient = &http.Client{Transport: failPagedFetch{next: http.DefaultTransport}}
	seedTracked(t, r, 10)
	logs := captureLogs(r)

	r.tick(context.Background())

	for _, id := range []int{14, 15, 16} {
		if r.store.PostID("test", id) == 0 {
			t.Errorf("message %d was not published", id)
		}
	}
	if r.store.Seen("test", 12) {
		t.Error("12 was never fetched, so it cannot have been published")
	}
	if !strings.Contains(logs.String(), "catch-up page fetch failed") {
		t.Errorf("expected a fetch-failed warning, got logs: %s", logs.String())
	}
}

func TestCatchUp_SyncReusesCatchUpPages(t *testing.T) {
	// Tracked 5 is inside the sync window; sync must continue from where catch-up stopped, not refetch.
	f := newFakeEitaa(t, map[string][]int{
		"":          {14, 15, 16},
		"before=14": {11, 12, 13},
		"before=11": {5, 7, 8, 9, 10},
	})
	r, pub := newPagedRunner(t, f)
	seedTracked(t, r, 5, 10)
	pub.deletes = nil

	r.tick(context.Background())

	if f.hits["before=14"] != 1 || f.hits["before=11"] != 1 {
		t.Errorf("each page should be fetched once, got hits %v", f.hits)
	}
	if len(pub.deletes) != 0 {
		t.Errorf("unexpected deletes: %v", pub.deletes)
	}
	if r.store.Seen("test", 7) {
		t.Error("7 is older than last seen and must not be published")
	}
}
