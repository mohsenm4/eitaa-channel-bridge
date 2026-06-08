package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadLegacyFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seen.json")
	legacy := `{"Merajyan":[2080,2083,2084]}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !s.Seen("Merajyan", 2083) {
		t.Error("expected 2083 seen")
	}
	if s.Seen("Merajyan", 9999) {
		t.Error("expected 9999 not seen")
	}
	if got := s.PostID("Merajyan", 2083); got != 0 {
		t.Errorf("legacy PostID = %d, want 0", got)
	}
	if got := s.Count("Merajyan"); got != 3 {
		t.Errorf("count = %d, want 3", got)
	}
}

func TestRoundTripNewFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seen.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.MarkWithPost("tesssst", 1, 5050)
	s.MarkWithPost("tesssst", 2, 5051)
	s.Mark("tesssst", 3) // seen but skipped
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.PostID("tesssst", 1); got != 5050 {
		t.Errorf("PostID(1) = %d, want 5050", got)
	}
	if got := s2.PostID("tesssst", 2); got != 5051 {
		t.Errorf("PostID(2) = %d, want 5051", got)
	}
	if got := s2.PostID("tesssst", 3); got != 0 {
		t.Errorf("PostID(3) = %d, want 0", got)
	}
	if !s2.Seen("tesssst", 3) {
		t.Error("Seen(3) should be true even with PostID 0")
	}
}

func TestLoadIntMapFormat_BackCompat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seen.json")
	old := `{"tesssst":{"54":18681,"58":18668,"3":0}}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.PostID("tesssst", 54); got != 18681 {
		t.Errorf("PostID(54) = %d, want 18681", got)
	}
	if !s.Seen("tesssst", 3) {
		t.Error("Seen(3) should be true")
	}
}

func TestFindRecentDuplicate_Window(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "seen.json"))
	t0 := time.Unix(1717851415, 0)
	s.MarkPublished("ch", 54, 18681, "abc123", t0)

	cases := []struct {
		name   string
		fp     string
		t      time.Time
		window time.Duration
		wantOK bool
	}{
		{"exact match same second", "abc123", t0, 10 * time.Second, true},
		{"3s drift within window", "abc123", t0.Add(3 * time.Second), 10 * time.Second, true},
		{"15s drift outside window", "abc123", t0.Add(15 * time.Second), 10 * time.Second, false},
		{"different fp", "xyz999", t0, 10 * time.Second, false},
		{"empty fp short-circuits", "", t0, 10 * time.Second, false},
		{"zero window short-circuits", "abc123", t0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			origID, origPost, ok := s.FindRecentDuplicate("ch", c.fp, c.t, c.window)
			if ok != c.wantOK {
				t.Errorf("ok=%v, want %v (origID=%d, origPost=%d)", ok, c.wantOK, origID, origPost)
			}
			if ok && (origID != 54 || origPost != 18681) {
				t.Errorf("found wrong entry: id=%d, post=%d", origID, origPost)
			}
		})
	}
}

func TestFindRecentDuplicate_IgnoresLegacyEntriesWithoutTS(t *testing.T) {
	// Legacy entries have FP="" and TS=0; they must not match dedupe checks.
	s, _ := Load(filepath.Join(t.TempDir(), "seen.json"))
	s.MarkWithPost("ch", 54, 18681) // no fp, no ts
	_, _, ok := s.FindRecentDuplicate("ch", "abc123", time.Now(), 60*time.Second)
	if ok {
		t.Error("legacy entries should not match dedupe")
	}
}
