// Package state persists the set of message IDs that have already been
// processed, so we never republish the same message twice.
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

// Store is a JSON-backed set of seen message IDs, keyed by channel.
type Store struct {
	path string
	data map[string]map[int]bool
}

// Load reads the store from disk. A missing file is treated as empty.
func Load(path string) (*Store, error) {
	s := &Store{path: path, data: map[string]map[int]bool{}}
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
	raw := map[string][]int{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	for ch, ids := range raw {
		set := make(map[int]bool, len(ids))
		for _, id := range ids {
			set[id] = true
		}
		s.data[ch] = set
	}
	return s, nil
}

// Seen reports whether a given message ID has already been processed.
func (s *Store) Seen(channel string, id int) bool {
	return s.data[channel] != nil && s.data[channel][id]
}

// Mark records a message ID as seen.
func (s *Store) Mark(channel string, id int) {
	if s.data[channel] == nil {
		s.data[channel] = map[int]bool{}
	}
	s.data[channel][id] = true
}

// Save writes the store atomically.
func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	out := map[string][]int{}
	for ch, set := range s.data {
		ids := make([]int, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		out[ch] = ids
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
