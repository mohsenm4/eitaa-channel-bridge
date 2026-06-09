package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"log/slog"
	"os"
	"os/signal"
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

// processBatch returns how many messages were handled (published or deliberately skipped).
// Publish failures are NOT counted, so the next tick retries them.
func (r *runner) processBatch(ctx context.Context, msgs []eitaa.Message) int {
	processed := 0
	for _, m := range msgs {
		if r.store.Seen(r.cfg.Source.Channel, m.ID) {
			continue
		}
		if r.processOne(ctx, m) {
			processed++
		}
	}
	return processed
}

// processOne returns true if the message reached a terminal state (published / inbox / skip).
// Returns false for transient failures so the next tick retries.
func (r *runner) processOne(ctx context.Context, m eitaa.Message) bool {
	routed := r.rt.Route(m)

	if tag := matchInboxHashtag(r.cfg.Publishing.InboxHashtags, routed.Hashtags); tag != "" {
		inboxPath := inboxFilePath(r.cfg.Storage.ArchiveFile)
		if err := appendInbox(inboxPath, m, tag); err != nil {
			r.log.Warn("inbox write failed — will retry next tick",
				"id", m.ID, "hashtag", tag, "err", err)
			return false
		}
		r.log.Info("inbox", "id", m.ID, "hashtag", tag, "link", m.Link, "file", inboxPath,
			"msg", "needs manual review — copy text into the homepage slider")
		r.markSeenAndPersist(m.ID)
		return true
	}

	ok, reason := publisher.ShouldPublish(r.cfg.Publishing, routed)
	if !ok {
		r.log.Info("skipped", "id", m.ID, "reason", reason)
		r.markSeenAndPersist(m.ID)
		return true
	}

	fp := fingerprint(m.Text)
	if origID, origPost, dup := r.store.FindRecentDuplicate(
		r.cfg.Source.Channel, fp, m.Date, r.cfg.Publishing.DedupeWindow,
	); dup {
		r.log.Info("dedupe",
			"id", m.ID, "duplicate_of", origID, "wp_post", origPost,
			"msg", "Eitaa double-send — same text within window, not republishing")
		r.markSeenAndPersist(m.ID)
		return true
	}

	postID, err := r.pub.Publish(ctx, routed)
	if err != nil {
		r.log.Error("publish failed", "id", m.ID, "err", err)
		return false
	}

	// Persist seen-state immediately so a crash before end-of-tick can't re-publish (issue #6).
	r.markPublishedAndPersist(m.ID, postID, fp, m.Date)
	if len(routed.Warnings) > 0 {
		if werr := appendWarning(r.cfg.Storage.ArchiveFile, routed); werr != nil {
			r.log.Warn("warnings log write failed", "id", m.ID, "err", werr)
		}
	}
	if aerr := jsonio.Append(r.cfg.Storage.ArchiveFile, m); aerr != nil {
		r.log.Warn("archive failed", "id", m.ID, "err", aerr)
	}
	r.log.Info("published",
		"id", m.ID, "wp_id", postID,
		"category", routed.CategoryFa,
		"title", utils.DisplayTitle(routed.Title, 50))
	return true
}

// markSeenAndPersist Marks the message as seen and flushes the state file.
// A save failure is logged loudly — the message will be re-processed on restart.
func (r *runner) markSeenAndPersist(id int) {
	r.store.Mark(r.cfg.Source.Channel, id)
	if err := r.store.Save(); err != nil {
		r.log.Warn("state save failed — message may be re-processed if bridge restarts",
			"id", id, "err", err)
	}
}

// markPublishedAndPersist records the post ID + fingerprint and flushes state in one shot.
func (r *runner) markPublishedAndPersist(id, postID int, fp string, t time.Time) {
	r.store.MarkPublished(r.cfg.Source.Channel, id, postID, fp, t)
	if err := r.store.Save(); err != nil {
		r.log.Warn("state save failed — message may be re-processed if bridge restarts",
			"id", id, "err", err)
	}
}

// fingerprint hashes the message text so duplicate detection ignores Eitaa ID variance.
func fingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

func (r *runner) tick(ctx context.Context) {
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	msgs, err := r.client.Fetch(fetchCtx, r.cfg.Source.Channel)
	if err != nil {
		r.log.Error("fetch failed", "err", err)
		return
	}
	// State is persisted per-message inside processOne; no end-of-tick save needed.
	if n := r.processBatch(ctx, msgs); n == 0 {
		r.log.Info("idle", "msg", "no new messages")
	}
}

// backfill walks Eitaa's ?before= pagination backwards up to max older messages.
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
		"inbox", strings.Join(cfg.Publishing.InboxHashtags, ","),
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

// matchInboxHashtag returns the first message hashtag that's in the operator's inbox list, or "".
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
