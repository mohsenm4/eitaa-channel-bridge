package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

// File appends each published message to a JSON Lines file.
// Useful as a dry-run / inspection target.
type File struct {
	path string
	mu   sync.Mutex
	f    *os.File
	enc  *json.Encoder
}

// NewFile opens (creating if needed) the JSONL file at path.
func NewFile(path string) (*File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &File{path: path, f: f, enc: json.NewEncoder(f)}, nil
}

// Name implements Publisher.
func (p *File) Name() string { return "file:" + p.path }

// Publish implements Publisher.
func (p *File) Publish(_ context.Context, msg router.Routed) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.enc.Encode(msg)
}

// Close implements Publisher.
func (p *File) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.f == nil {
		return nil
	}
	err := p.f.Close()
	p.f = nil
	return err
}
