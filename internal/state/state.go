// Package state persists processed message IDs per channel with the post ID, text fingerprint, and time.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// Entry records what we did with one Eitaa message and how to recognise its duplicates / edits later.
type Entry struct {
	PostID  int    `json:"post,omitempty"` // 0 = seen but no post (skipped, inbox, or pre-fingerprint)
	FP      string `json:"fp,omitempty"`   // sha256(text)[:16]; empty for legacy / inbox / skip entries
	TS      int64  `json:"ts,omitempty"`   // msg.Date.Unix(); 0 means "no time on record"
	Deleted bool   `json:"deleted,omitempty"` // true once we've propagated a source-side deletion to the target.
}

type Store struct {
	path string
	data map[string]map[int]Entry // channel → eitaa id → entry
}

// Load reads the store (missing file = empty); accepts the new {post,fp,ts} format and two older shapes.
func Load(path string) (*Store, error) {
	s := &Store{path: path, data: map[string]map[int]Entry{}}
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
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	for ch, innerRaw := range top {
		s.data[ch] = parseChannel(innerRaw)
	}
	return s, nil
}

// parseChannel decodes one channel's slice (legacy []int) or map (any-version) shape.
func parseChannel(raw json.RawMessage) map[int]Entry {
	var ids []int
	if err := json.Unmarshal(raw, &ids); err == nil {
		m := make(map[int]Entry, len(ids))
		for _, id := range ids {
			m[id] = Entry{}
		}
		return m
	}
	var inner map[string]json.RawMessage
	if err := json.Unmarshal(raw, &inner); err != nil {
		return map[int]Entry{}
	}
	m := make(map[int]Entry, len(inner))
	for idStr, val := range inner {
		id, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}
		var e Entry
		if err := json.Unmarshal(val, &e); err == nil {
			m[id] = e
			continue
		}
		var postID int
		if err := json.Unmarshal(val, &postID); err == nil {
			m[id] = Entry{PostID: postID}
		}
	}
	return m
}

func (s *Store) Seen(channel string, id int) bool {
	_, ok := s.data[channel][id]
	return ok
}

func (s *Store) Count(channel string) int {
	return len(s.data[channel])
}

// Mark records a message as seen with no metadata (skipped / inbox / post-less targets).
func (s *Store) Mark(channel string, id int) {
	s.ensure(channel)
	if _, ok := s.data[channel][id]; !ok {
		s.data[channel][id] = Entry{}
	}
}

// MarkWithPost records seen + the post ID, no fingerprint (kept for backward compat).
func (s *Store) MarkWithPost(channel string, eitaaID, postID int) {
	s.ensure(channel)
	s.data[channel][eitaaID] = Entry{PostID: postID}
}

// MarkPublished records seen + post ID + fingerprint + msg time, the richest form (used for dedupe / edit detection).
func (s *Store) MarkPublished(channel string, eitaaID, postID int, fp string, t time.Time) {
	s.ensure(channel)
	s.data[channel][eitaaID] = Entry{PostID: postID, FP: fp, TS: t.Unix()}
}

// PostID returns the post ID a message produced, or 0 if unseen / post-less.
func (s *Store) PostID(channel string, eitaaID int) int {
	return s.data[channel][eitaaID].PostID
}

// Get returns a copy of the entry for an id (zero-value if not present).
func (s *Store) Get(channel string, id int) Entry {
	return s.data[channel][id]
}

// UpdateFP rewrites the fingerprint of an existing entry — used after a source-side edit is mirrored.
func (s *Store) UpdateFP(channel string, id int, fp string) {
	if _, ok := s.data[channel][id]; !ok {
		return
	}
	e := s.data[channel][id]
	e.FP = fp
	s.data[channel][id] = e
}

// MarkDeleted flags an entry as deleted-in-source so we don't try to delete the WP post twice.
func (s *Store) MarkDeleted(channel string, id int) {
	if _, ok := s.data[channel][id]; !ok {
		return
	}
	e := s.data[channel][id]
	e.Deleted = true
	s.data[channel][id] = e
}

// TrackedInRange returns ids of entries with a WP post (PostID>0), within [minID,maxID], not yet marked Deleted.
// These are the candidates for edit / deletion comparison against the latest fetched page.
func (s *Store) TrackedInRange(channel string, minID, maxID int) []int {
	if minID > maxID {
		return nil
	}
	var out []int
	for id, e := range s.data[channel] {
		if e.PostID <= 0 || e.Deleted {
			continue
		}
		if id < minID || id > maxID {
			continue
		}
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// LatestTrackedTS returns the highest TS among non-deleted entries with a WP post — used to pick hot/cold polling.
// Returns zero time if there are no tracked entries.
func (s *Store) LatestTrackedTS(channel string) time.Time {
	var latest int64
	for _, e := range s.data[channel] {
		if e.PostID <= 0 || e.Deleted || e.TS == 0 {
			continue
		}
		if e.TS > latest {
			latest = e.TS
		}
	}
	if latest == 0 {
		return time.Time{}
	}
	return time.Unix(latest, 0)
}

// FindRecentDuplicate scans channel for a prior entry with the same fp whose ts is within window of msgTime.
// Returns the original eitaa id + post id if found. Empty fp or zero window short-circuits to "not found".
func (s *Store) FindRecentDuplicate(channel, fp string, msgTime time.Time, window time.Duration) (eitaaID, postID int, ok bool) {
	if fp == "" || window <= 0 {
		return 0, 0, false
	}
	msgTS := msgTime.Unix()
	for id, e := range s.data[channel] {
		if e.FP != fp || e.TS == 0 {
			continue
		}
		diff := msgTS - e.TS
		if diff < 0 {
			diff = -diff
		}
		if time.Duration(diff)*time.Second <= window {
			return id, e.PostID, true
		}
	}
	return 0, 0, false
}

func (s *Store) ensure(channel string) {
	if s.data[channel] == nil {
		s.data[channel] = map[int]Entry{}
	}
}

// Save writes the new {post,fp,ts} format atomically; key order is deterministic for small diffs.
func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	out := map[string]map[string]Entry{}
	for ch, m := range s.data {
		ids := make([]int, 0, len(m))
		for id := range m {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		inner := make(map[string]Entry, len(ids))
		for _, id := range ids {
			inner[strconv.Itoa(id)] = m[id]
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
