package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/jsonio"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/publisher"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/state"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/utils"
)

type runner struct {
	cfg    *config.Config
	log    *slog.Logger
	store  *state.Store
	pub    publisher.Publisher
	client *eitaa.Client
	rt     *router.Router
}

func newRunner(cfg *config.Config, log *slog.Logger) (*runner, error) {
	store, err := state.Load(cfg.Storage.SeenFile)
	if err != nil {
		return nil, err
	}
	pub, err := publisher.New(cfg.Target, log)
	if err != nil {
		return nil, err
	}
	return &runner{
		cfg:    cfg,
		log:    log,
		store:  store,
		pub:    pub,
		client: eitaa.New(),
		rt:     router.New(cfg.Source.Channel, cfg.Publishing.Categories, cfg.Publishing.Default),
	}, nil
}

func (r *runner) Close() error { return r.pub.Close() }

// processBatch routes/filters/publishes msgs and returns how many were handled
// (published or deliberately skipped). Publish failures are NOT counted so the
// next tick retries them.
func (r *runner) processBatch(ctx context.Context, msgs []eitaa.Message) int {
	processed := 0
	for _, m := range msgs {
		if r.store.Seen(r.cfg.Source.Channel, m.ID) {
			continue
		}
		routed := r.rt.Route(m)
		if tag := matchInboxHashtag(r.cfg.Publishing.InboxHashtags, routed.Hashtags); tag != "" {
			inboxPath := inboxFilePath(r.cfg.Storage.ArchiveFile)
			if err := appendInbox(inboxPath, m, tag); err != nil {
				r.log.Warn("inbox write failed — will retry next tick",
					"id", m.ID, "hashtag", tag, "err", err)
				continue
			}
			r.log.Info("inbox", "id", m.ID, "hashtag", tag, "link", m.Link, "file", inboxPath,
				"msg", "needs manual review — copy text into the homepage slider")
			r.store.Mark(r.cfg.Source.Channel, m.ID)
			processed++
			continue
		}
		ok, reason := publisher.ShouldPublish(r.cfg.Publishing, routed)
		if !ok {
			r.log.Info("skipped", "id", m.ID, "reason", reason)
			r.store.Mark(r.cfg.Source.Channel, m.ID)
			processed++
			continue
		}
		postID, err := r.pub.Publish(ctx, routed)
		if err != nil {
			r.log.Error("publish failed", "id", m.ID, "err", err)
			continue
		}
		if len(routed.Warnings) > 0 {
			if err := appendWarning(r.cfg.Storage.ArchiveFile, routed); err != nil {
				r.log.Warn("warnings log write failed", "id", m.ID, "err", err)
			}
		}
		if err := jsonio.Append(r.cfg.Storage.ArchiveFile, m); err != nil {
			r.log.Warn("archive failed", "id", m.ID, "err", err)
		}
		if postID > 0 {
			r.store.MarkWithPost(r.cfg.Source.Channel, m.ID, postID)
		} else {
			r.store.Mark(r.cfg.Source.Channel, m.ID)
		}
		processed++
		r.log.Info("published",
			"id", m.ID,
			"wp_id", postID,
			"category", routed.CategoryFa,
			"title", utils.DisplayTitle(routed.Title, 50))
	}
	return processed
}

func (r *runner) tick(ctx context.Context) {
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	msgs, err := r.client.Fetch(fetchCtx, r.cfg.Source.Channel)
	if err != nil {
		r.log.Error("fetch failed", "err", err)
		return
	}
	n := r.processBatch(ctx, msgs)
	if n > 0 {
		if err := r.store.Save(); err != nil {
			r.log.Warn("state save failed", "err", err)
		}
	} else {
		r.log.Info("idle", "msg", "no new messages")
	}
}

// backfill walks ?before= pagination backwards up to max messages.
func (r *runner) backfill(ctx context.Context, max int) {
	r.log.Info("backfill start", "max", max)

	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	page, err := r.client.Fetch(fetchCtx, r.cfg.Source.Channel)
	cancel()
	if err != nil {
		r.log.Error("backfill initial fetch failed", "err", err)
		return
	}
	collected := append([]eitaa.Message{}, page...)

	for len(collected) < max {
		if len(page) == 0 {
			break
		}
		oldest := page[0].ID
		fctx, fcancel := context.WithTimeout(ctx, 30*time.Second)
		next, err := r.client.FetchBefore(fctx, r.cfg.Source.Channel, oldest)
		fcancel()
		if err != nil {
			r.log.Error("backfill page fetch failed", "before", oldest, "err", err)
			break
		}
		if len(next) == 0 {
			break
		}
		collected = append(next, collected...)
		page = next
	}
	if len(collected) > max {
		collected = collected[len(collected)-max:]
	}
	n := r.processBatch(ctx, collected)
	if err := r.store.Save(); err != nil {
		r.log.Warn("state save failed", "err", err)
	}
	r.log.Info("backfill done", "fetched", len(collected), "processed", n)
}

func (r *runner) Run(ctx context.Context) {
	if r.store.Count(r.cfg.Source.Channel) == 0 && r.cfg.Source.BackfillMax > 0 {
		r.backfill(ctx, r.cfg.Source.BackfillMax)
	}

	r.tick(ctx)
	ticker := time.NewTicker(r.cfg.Source.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.log.Info("stopping")
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func runRun(log *slog.Logger, args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	envPath := fs.String("env", defaultEnvPath, "path to .env file (optional)")
	_ = fs.Parse(args)

	cfg := config.MustLoad(*envPath)
	r, err := newRunner(cfg, log)
	if err != nil {
		utils.Fatal("init: %v", err)
	}
	defer r.Close()

	log.Info("starting",
		"source", cfg.Source.Channel,
		"target", r.pub.Name(),
		"interval", cfg.Source.PollInterval.String(),
		"backfill_max", cfg.Source.BackfillMax,
		"categories", strings.Join(categoryTags(cfg.Publishing.Categories), ","),
		"skip", strings.Join(cfg.Publishing.SkipHashtags, ","),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	r.Run(ctx)
}

func categoryTags(cats []config.Category) []string {
	out := make([]string, len(cats))
	for i, c := range cats {
		out[i] = c.Hashtag
	}
	return out
}

// matchInboxHashtag returns the first message hashtag that appears in
// the operator's inbox list, or "" if none does. Used to route messages
// that need a human to look at them (e.g. #حدیث, where the homepage
// slider is hand-curated) instead of being auto-published.
func matchInboxHashtag(inbox, msgTags []string) string {
	if len(inbox) == 0 || len(msgTags) == 0 {
		return ""
	}
	set := make(map[string]bool, len(inbox))
	for _, t := range inbox {
		set[t] = true
	}
	for _, t := range msgTags {
		if set[t] {
			return t
		}
	}
	return ""
}

// inboxFilePath puts inbox.log next to the archive (typically data/).
func inboxFilePath(archiveFile string) string {
	return filepath.Join(filepath.Dir(archiveFile), "inbox.log")
}

// appendInbox writes one fully self-contained block per pending message:
// timestamp + hashtag + eitaa link + full body. Format chosen to be
// scannable with `cat`/`tail` — the operator opens the file, copies the
// text into the homepage slider manually, optionally trims handled
// entries from the top.
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

// appendWarning writes one human-readable line per problematic message
// to data/warnings.log (alongside the archive). Format chosen to be
// readable with `cat` — not JSON — so the operator can quickly skim it
// and report issues back to the channel author.
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
