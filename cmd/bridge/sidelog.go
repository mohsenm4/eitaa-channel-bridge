package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

// inboxFilePath puts inbox.log next to the archive (typically data/).
func inboxFilePath(archiveFile string) string {
	return filepath.Join(filepath.Dir(archiveFile), "inbox.log")
}

// appendInbox queues one unpublished message — timestamp, hashtag, link, full body — for manual review.
func appendInbox(path string, m eitaa.Message, hashtag string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f,
		"────────────────────────────────────────\n"+
			"received: %s\n"+
			"hashtag:  #%s\n"+
			"eitaa:    %s\n"+
			"────────────────────────────────────────\n"+
			"%s\n\n",
		time.Now().Format("2006-01-02 15:04:05"),
		hashtag, m.Link, strings.TrimSpace(m.Text))
	return err
}

// appendWarning records one published-but-imperfect message to data/warnings.log for follow-up with the author.
func appendWarning(archiveFile string, r router.Routed) error {
	path := filepath.Join(filepath.Dir(archiveFile), "warnings.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s  msg=%d  link=%s  warnings=%q\n",
		time.Now().Format("2006-01-02 15:04:05"),
		r.ID, r.Link, strings.Join(r.Warnings, "؛ "))
	return err
}
