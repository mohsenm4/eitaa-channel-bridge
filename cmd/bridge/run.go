package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"math"
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
	home   *homepageClient
}

func newRunner(cfg *config.Config, log *slog.Logger) (*runner, error) {
	store, err := state.Load(cfg.Storage.SeenFile)
	if err != nil {
		return nil, err
	}
	pub := publisher.NewWordPress(cfg.WordPress, log)
	var home *homepageClient
	if cfg.Homepage.PageID > 0 {
		home = newHomepageClient(cfg.WordPress.URL, cfg.WordPress.Username, cfg.WordPress.AppPassword)
	}
	return &runner{
		cfg:    cfg,
		log:    log,
		store:  store,
		pub:    pub,
		client: eitaa.New(),
		rt:     router.New(cfg.Source.Channel, cfg.Publishing.Categories, cfg.Publishing.Default),
		home:   home,
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
	r.maybeUpdateHomepagePoster(ctx, routed, postID)
	return true
}

// discoverHomepage fills in cfg.Homepage.{PageID,PosterLinks} from WordPress
// itself so the operator doesn't have to write either into .env. Manual entries
// always win — discovery only populates the unset fields. A failure here is
// non-fatal: the bridge logs and continues publishing without poster updates.
func (r *runner) discoverHomepage(ctx context.Context) {
	if r.home == nil {
		// No app password / no WP target → homepage features disabled at startup.
		if r.cfg.WordPress.URL == "" || r.cfg.WordPress.Username == "" {
			return
		}
		r.home = newHomepageClient(r.cfg.WordPress.URL, r.cfg.WordPress.Username, r.cfg.WordPress.AppPassword)
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if r.cfg.Homepage.PageID == 0 {
		pid, err := r.home.detectFrontPageID(dctx)
		if err != nil {
			r.log.Warn("homepage discovery: front-page id lookup failed — set HOMEPAGE_PAGE_ID manually to enable poster updates",
				"err", err)
			r.home = nil
			return
		}
		if pid == 0 {
			r.log.Info("homepage discovery: site uses the blog layout, no static front page — skipping")
			r.home = nil
			return
		}
		r.cfg.Homepage.PageID = pid
		r.log.Info("homepage page id discovered", "page_id", pid)
	}

	if len(r.cfg.Homepage.PosterLinks) > 0 {
		return // manual map wins; trust the operator.
	}
	slides, err := r.home.listSlides(dctx, r.cfg.Homepage.PageID)
	if err != nil {
		r.log.Warn("homepage discovery: list-slides failed — poster updates disabled this run", "err", err)
		return
	}
	mapping := map[string]string{}
	for _, s := range slides {
		if s.Type != "av_slide" || s.ImageID == "" {
			continue
		}
		var imgID int
		if _, err := fmt.Sscanf(s.ImageID, "%d", &imgID); err != nil || imgID <= 0 {
			continue
		}
		slug, err := r.home.mediaSlug(dctx, imgID)
		if err != nil || slug == "" {
			continue
		}
		// Match if the image slug contains the category's WP slug. So
		// `hemayat-khedmat-poster` matches category slug `hemayat-khedmat`,
		// and `qarz-al-hasaneh.png` (no suffix) matches `qarz-al-hasaneh`.
		for _, cat := range r.cfg.Publishing.Categories {
			if cat.Slug == "" {
				continue
			}
			if !strings.Contains(slug, cat.Slug) {
				continue
			}
			if existing, dup := mapping[cat.Hashtag]; dup {
				r.log.Warn("homepage discovery: more than one poster matched a category — keeping the first",
					"hashtag", cat.Hashtag, "first_uid", existing, "skipped_uid", s.UID, "image_slug", slug)
				continue
			}
			mapping[cat.Hashtag] = s.UID
			r.log.Info("poster auto-mapped",
				"hashtag", cat.Hashtag, "slide_uid", s.UID, "image_id", imgID, "image_slug", slug)
		}
	}
	r.cfg.Homepage.PosterLinks = mapping
	if len(mapping) == 0 {
		r.log.Info("homepage discovery: no poster image slug matched any category — name posters like \"<category-slug>-something.jpg\" to enable")
	}
}

// maybeUpdateHomepagePoster rewrites the link of the homepage poster slide
// mapped to this routing hashtag so it points at the freshly-published post.
// Lookup is by routed.Category (slug) so renaming the WP-side label can't
// break the wiring. Non-fatal: the post is already on WP; if this fails the
// poster just keeps its prior link until the next publish in the same category.
func (r *runner) maybeUpdateHomepagePoster(ctx context.Context, routed router.Routed, postID int) {
	if r.home == nil || len(r.cfg.Homepage.PosterLinks) == 0 || r.cfg.Homepage.PageID <= 0 {
		return
	}
	uid, ok := r.cfg.Homepage.PosterLinks[routed.Category]
	if !ok {
		// Try matching by hashtag too — operator may have keyed POSTER_LINKS
		// off the channel hashtag rather than the WP slug.
		for _, tag := range routed.Hashtags {
			if u, found := r.cfg.Homepage.PosterLinks[tag]; found {
				uid = u
				ok = true
				break
			}
		}
	}
	if !ok {
		return
	}
	link := fmt.Sprintf("%s/?p=%d", strings.TrimRight(r.cfg.WordPress.URL, "/"), postID)
	uctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := r.home.updatePosterLink(uctx, r.cfg.Homepage.PageID, uid, link); err != nil {
		r.log.Warn("homepage poster update failed — post is live, poster keeps prior link",
			"category", routed.Category, "slide_uid", uid, "wp_id", postID, "err", err)
		return
	}
	r.log.Info("homepage poster updated",
		"category", routed.Category, "slide_uid", uid, "wp_id", postID, "link", link)
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
	r.syncEditsAndDeletes(ctx, msgs)
	// State is persisted per-message inside processOne; no end-of-tick save needed.
	if n := r.processBatch(ctx, msgs); n == 0 {
		r.log.Info("idle", "msg", "no new messages")
	}
}

// syncEditsAndDeletes mirrors author actions in the source channel onto the WP target.
// For each tracked message (PostID>0) whose ID is still inside the fetched page:
//   - present in fetch with a different fingerprint → push an edit
//   - absent from fetch → push a delete (one-shot, idempotent via Deleted flag)
//
// Message age does NOT gate this check — EditWindow only controls polling cadence.
// Late edits / deletes made hours later are still mirrored on the next cold-mode tick.
func (r *runner) syncEditsAndDeletes(ctx context.Context, msgs []eitaa.Message) {
	if len(msgs) == 0 {
		return
	}
	minID := msgs[0].ID
	fetched := make(map[int]eitaa.Message, len(msgs))
	for _, m := range msgs {
		fetched[m.ID] = m
		if m.ID < minID {
			minID = m.ID
		}
	}

	// Upper bound is unbounded: a deleted "latest" message is missing from the page but its ID > min(fetched),
	// so we must still consider it. The lower bound is the oldest visible — anything older is off-page (can't tell).
	tracked := r.store.TrackedInRange(r.cfg.Source.Channel, minID, math.MaxInt)
	if len(tracked) == 0 {
		return
	}

	for _, id := range tracked {
		entry := r.store.Get(r.cfg.Source.Channel, id)
		if m, ok := fetched[id]; ok {
			r.maybeSyncEdit(ctx, m, entry)
		} else {
			r.syncDeletion(ctx, id, entry)
		}
	}
}

// maybeSyncEdit pushes an Update to the target when the fetched message's text fingerprint differs from the stored one.
// Stored FP is updated only on success, so transient publisher failures get retried next tick.
func (r *runner) maybeSyncEdit(ctx context.Context, m eitaa.Message, entry state.Entry) {
	newFP := fingerprint(m.Text)
	if entry.FP == newFP {
		return
	}
	if entry.FP == "" {
		// Pre-feature entries had no FP; seed silently rather than spam an edit on every tracked old message.
		r.store.UpdateFP(r.cfg.Source.Channel, m.ID, newFP)
		if err := r.store.Save(); err != nil {
			r.log.Warn("state save failed after fp seed", "id", m.ID, "err", err)
		}
		return
	}

	routed := r.rt.Route(m)
	// If the edit added a skip-hashtag, treat it as a deletion to keep WP in sync with author intent.
	if ok, reason := publisher.ShouldPublish(r.cfg.Publishing, routed); !ok {
		r.log.Info("edit converts message to non-publishable — deleting target",
			"id", m.ID, "wp_id", entry.PostID, "reason", reason)
		r.syncDeletion(ctx, m.ID, entry)
		return
	}
	if err := r.pub.Update(ctx, entry.PostID, routed); err != nil {
		r.log.Error("edit sync failed — will retry next tick",
			"id", m.ID, "wp_id", entry.PostID, "err", err)
		return
	}
	r.store.UpdateFP(r.cfg.Source.Channel, m.ID, newFP)
	if err := r.store.Save(); err != nil {
		r.log.Warn("state save failed after edit sync", "id", m.ID, "err", err)
	}
	r.log.Info("edit synced", "id", m.ID, "wp_id", entry.PostID,
		"title", utils.DisplayTitle(routed.Title, 50))
}

// syncDeletion mirrors a source-side deletion to the target, then marks the entry so we don't retry forever.
func (r *runner) syncDeletion(ctx context.Context, id int, entry state.Entry) {
	if err := r.pub.Delete(ctx, entry.PostID); err != nil {
		r.log.Error("deletion sync failed — will retry next tick",
			"id", id, "wp_id", entry.PostID, "err", err)
		return
	}
	r.store.MarkDeleted(r.cfg.Source.Channel, id)
	if err := r.store.Save(); err != nil {
		r.log.Warn("state save failed after deletion sync", "id", id, "err", err)
	}
	r.log.Info("deletion synced", "id", id, "wp_id", entry.PostID)
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
	go r.runHealthcheckLoop(ctx)
	r.discoverHomepage(ctx)
	if r.store.Count(r.cfg.Source.Channel) == 0 && r.cfg.Source.BackfillMax > 0 {
		r.backfill(ctx, r.cfg.Source.BackfillMax)
	}
	for {
		r.tick(ctx)
		next := r.nextInterval()
		r.log.Info("scheduled next tick", "in", next.String(), "mode", r.pollMode())
		t := time.NewTimer(next)
		select {
		case <-ctx.Done():
			t.Stop()
			r.log.Info("stopping")
			return
		case <-t.C:
		}
	}
}

// nextInterval returns the hot interval while any tracked message is within EditWindow, else the cold interval.
func (r *runner) nextInterval() time.Duration {
	latest := r.store.LatestTrackedTS(r.cfg.Source.Channel)
	if !latest.IsZero() && time.Since(latest) < r.cfg.Source.EditWindow {
		return r.cfg.Source.PollHot
	}
	return r.cfg.Source.PollCold
}

func (r *runner) pollMode() string {
	if r.nextInterval() == r.cfg.Source.PollHot {
		return "hot"
	}
	return "cold"
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
		"poll_cold", cfg.Source.PollCold.String(),
		"poll_hot", cfg.Source.PollHot.String(),
		"edit_window", cfg.Source.EditWindow.String(),
		"backfill_max", cfg.Source.BackfillMax,
		"categories", strings.Join(categoryTags(cfg.Publishing.Categories), ","),
		"inbox", strings.Join(cfg.Publishing.InboxHashtags, ","),
		"skip", strings.Join(cfg.Publishing.SkipHashtags, ","),
		"homepage_page", cfg.Homepage.PageID,
		"posters", len(cfg.Homepage.PosterLinks),
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
