package state

import (
	"os"
	"path/filepath"
	"testing"
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
