// Package state persists processed message IDs per channel and their resulting target post IDs.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

type Store struct {
	path string
	// channel → eitaa message ID → target post ID (0 = seen but no post)
	data map[string]map[int]int
}

// Load reads the store (missing file = empty); accepts both the map and the legacy []int formats.
func Load(path string) (*Store, error) {
	s := &Store{path: path, data: map[string]map[int]int{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	if len(b) == 0 {
		return s, nil
	}
	// Try new format: {"channel": {"123": 4567, "124": 0}}.
	rawMap := map[string]map[int]int{}
	if err := json.Unmarshal(b, &rawMap); err == nil {
		for ch, m := range rawMap {
			s.data[ch] = m
		}
		return s, nil
	}
	// Fall back to legacy format: {"channel": [1, 2, 3]}.
	rawList := map[string][]int{}
	if err := json.Unmarshal(b, &rawList); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	for ch, ids := range rawList {
		m := make(map[int]int, len(ids))
		for _, id := range ids {
			m[id] = 0
		}
		s.data[ch] = m
	}
	return s, nil
}

func (s *Store) Seen(channel string, id int) bool {
	if s.data[channel] == nil {
		return false
	}
	_, ok := s.data[channel][id]
	return ok
}

func (s *Store) Count(channel string) int {
	return len(s.data[channel])
}

// Mark records a message as seen without a post ID (skipped messages or post-less targets).
func (s *Store) Mark(channel string, id int) {
	if s.data[channel] == nil {
		s.data[channel] = map[int]int{}
	}
	if _, ok := s.data[channel][id]; !ok {
		s.data[channel][id] = 0
	}
}

// MarkWithPost records a message as seen and remembers the post ID it produced (used by #آرشیو reply chains).
func (s *Store) MarkWithPost(channel string, eitaaID, postID int) {
	if s.data[channel] == nil {
		s.data[channel] = map[int]int{}
	}
	s.data[channel][eitaaID] = postID
}

// PostID returns the post ID an eitaa message produced, or 0 if unseen or post-less.
func (s *Store) PostID(channel string, eitaaID int) int {
	if s.data[channel] == nil {
		return 0
	}
	return s.data[channel][eitaaID]
}

// Save writes the store atomically (write-then-rename).
func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	// Marshal with deterministic key order so diffs stay small.
	out := map[string]map[string]int{}
	for ch, m := range s.data {
		ids := make([]int, 0, len(m))
		for id := range m {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		inner := make(map[string]int, len(ids))
		for _, id := range ids {
			inner[fmt.Sprintf("%d", id)] = m[id]
		}
		out[ch] = inner
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
