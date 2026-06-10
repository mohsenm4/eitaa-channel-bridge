package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/state"
)

// mockPub is a Publisher stub for runner tests: returns sequential post IDs,
// or an error if fail is set.
type mockPub struct {
	nextID int
	fail   bool
}

func (p *mockPub) Name() string { return "mock" }
func (p *mockPub) Close() error { return nil }
func (p *mockPub) Publish(context.Context, router.Routed) (int, error) {
	if p.fail {
		return 0, errors.New("simulated publish failure")
	}
	p.nextID++
	return p.nextID, nil
}
func (p *mockPub) Update(context.Context, int, router.Routed) error { return nil }
func (p *mockPub) Delete(context.Context, int) error                { return nil }

func newTestRunner(t *testing.T, pub *mockPub) (*runner, string) {
	t.Helper()
	dir := t.TempDir()
	seenPath := filepath.Join(dir, "seen.json")
	archivePath := filepath.Join(dir, "messages.jsonl")

	store, err := state.Load(seenPath)
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	cfg := &config.Config{
		Source: config.Source{Channel: "test"},
		Publishing: config.Publishing{
			Categories: []config.Category{
				{Hashtag: "test", Slug: "test", Label: "Test"},
			},
			InboxHashtags: []string{"inbox"},
			DedupeWindow:  10 * time.Second,
		},
		Storage: config.Storage{SeenFile: seenPath, ArchiveFile: archivePath},
	}
	r := &runner{
		cfg:   cfg,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		store: store,
		pub:   pub,
		rt:    router.New("test", cfg.Publishing.Categories, nil),
	}
	return r, seenPath
}

// reloadSeen mimics a bridge restart by re-reading the state file from disk.
func reloadSeen(t *testing.T, path string) *state.Store {
	t.Helper()
	s, err := state.Load(path)
	if err != nil {
		t.Fatalf("state.Load reload: %v", err)
	}
	return s
}

func TestProcessOne_PersistsAfterPublish(t *testing.T) {
	pub := &mockPub{nextID: 1000}
	r, seenPath := newTestRunner(t, pub)

	msg := eitaa.Message{
		ID:      42,
		Channel: "test",
		Text:    "📌 hello world\n\n#test",
		Date:    time.Now(),
	}
	if !r.processOne(context.Background(), msg) {
		t.Fatal("processOne returned false for a successful publish")
	}

	reloaded := reloadSeen(t, seenPath)
	if !reloaded.Seen("test", 42) {
		t.Error("msg 42 not persisted after publish — a crash here would re-publish")
	}
	if got := reloaded.PostID("test", 42); got != 1001 {
		t.Errorf("reloaded PostID = %d, want 1001", got)
	}
}

func TestProcessOne_PersistsAfterSkip(t *testing.T) {
	r, seenPath := newTestRunner(t, &mockPub{})

	// No matching category → skipped by ShouldPublish.
	msg := eitaa.Message{
		ID:      43,
		Channel: "test",
		Text:    "no hashtag here",
		Date:    time.Now(),
	}
	if !r.processOne(context.Background(), msg) {
		t.Fatal("processOne returned false for skip path")
	}
	if !reloadSeen(t, seenPath).Seen("test", 43) {
		t.Error("skipped message not persisted")
	}
}

func TestProcessOne_PersistsAfterInbox(t *testing.T) {
	r, seenPath := newTestRunner(t, &mockPub{})

	msg := eitaa.Message{
		ID:      44,
		Channel: "test",
		Text:    "some text\n\n#inbox",
		Date:    time.Now(),
	}
	if !r.processOne(context.Background(), msg) {
		t.Fatal("processOne returned false for inbox path")
	}
	if !reloadSeen(t, seenPath).Seen("test", 44) {
		t.Error("inbox-routed message not persisted")
	}
}

func TestProcessOne_DoesNotPersistOnPublishFailure(t *testing.T) {
	// Publisher fails → message should be retried next tick, so NOT marked seen.
	r, seenPath := newTestRunner(t, &mockPub{fail: true})

	msg := eitaa.Message{
		ID:      45,
		Channel: "test",
		Text:    "📌 retry me\n\n#test",
		Date:    time.Now(),
	}
	if r.processOne(context.Background(), msg) {
		t.Fatal("processOne returned true despite publish failure")
	}
	if reloadSeen(t, seenPath).Seen("test", 45) {
		t.Error("failed publish must not be marked seen — would silently drop on retry")
	}
}
