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

	// publishFailStreak counts consecutive Publish failures; flips healthcheck to DOWN when it crosses the threshold.
	publishFailStreak int
	// publishFailAlerted is true after we've sent the /fail ping for the current streak — avoids spamming the ping.
	publishFailAlerted bool
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

// processBatch returns how many messages were handled (published or skipped); publish failures aren't counted so the next tick retries.
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

// processOne returns true on a terminal state (published / inbox / skip); false on transient failures for retry next tick.
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
		r.recordPublishFailure(ctx)
		return false
	}
	r.recordPublishSuccess()

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

const homepageDiscoveryTimeout = 30 * time.Second

// discoverHomepage auto-fills cfg.Homepage.{PageID,PosterLinks} from WP; manual .env entries win and failures are non-fatal.
func (r *runner) discoverHomepage(ctx context.Context) {
	if !r.ensureHomepageClient() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, homepageDiscoveryTimeout)
	defer cancel()

	if !r.discoverFrontPageID(ctx) {
		return
	}
	r.autoMapPostersToCategories(ctx)
}

// ensureHomepageClient lazily builds r.home; returns false when WP isn't configured.
func (r *runner) ensureHomepageClient() bool {
	if r.home != nil {
		return true
	}
	if r.cfg.WordPress.URL == "" || r.cfg.WordPress.Username == "" {
		return false
	}
	r.home = newHomepageClient(r.cfg.WordPress.URL, r.cfg.WordPress.Username, r.cfg.WordPress.AppPassword)
	return true
}

// discoverFrontPageID asks WP for the static front-page id; returns false to disable homepage features this run.
func (r *runner) discoverFrontPageID(ctx context.Context) bool {
	if r.cfg.Homepage.PageID != 0 {
		return true
	}
	pageID, err := r.home.detectFrontPageID(ctx)
	if err != nil {
		r.log.Warn("homepage discovery: front-page id lookup failed — set HOMEPAGE_PAGE_ID manually to enable poster updates", "err", err)
		r.home = nil
		return false
	}
	if pageID == 0 {
		r.log.Info("homepage discovery: site uses the blog layout, no static front page — skipping")
		r.home = nil
		return false
	}
	r.cfg.Homepage.PageID = pageID
	r.log.Info("homepage page id discovered", "page_id", pageID)
	return true
}

// autoMapPostersToCategories pairs each av_slide poster with a category whose WP slug appears inside the image slug.
func (r *runner) autoMapPostersToCategories(ctx context.Context) {
	if len(r.cfg.Homepage.PosterLinks) > 0 {
		return
	}
	slides, err := r.home.listSlides(ctx, r.cfg.Homepage.PageID)
	if err != nil {
		r.log.Warn("homepage discovery: list-slides failed — poster updates disabled this run", "err", err)
		return
	}
	mapping := map[string]string{}
	for _, slide := range slides {
		r.mapSlideToCategory(ctx, slide, mapping)
	}
	r.cfg.Homepage.PosterLinks = mapping
	if len(mapping) == 0 {
		r.log.Info("homepage discovery: no poster image slug matched any category — name posters like \"<category-slug>-something.jpg\" to enable")
	}
}

// mapSlideToCategory records the first category whose slug appears in the slide's image slug; later dupes are warned and skipped.
func (r *runner) mapSlideToCategory(ctx context.Context, slide Slide, mapping map[string]string) {
	if slide.Type != "av_slide" || slide.ImageID == "" {
		return
	}
	var imageID int
	if _, err := fmt.Sscanf(slide.ImageID, "%d", &imageID); err != nil || imageID <= 0 {
		return
	}
	imageSlug, err := r.home.mediaSlug(ctx, imageID)
	if err != nil || imageSlug == "" {
		return
	}
	for _, cat := range r.cfg.Publishing.Categories {
		if cat.Slug == "" || !strings.Contains(imageSlug, cat.Slug) {
			continue
		}
		if firstUID, dup := mapping[cat.Hashtag]; dup {
			r.log.Warn("homepage discovery: more than one poster matched a category — keeping the first",
				"hashtag", cat.Hashtag, "first_uid", firstUID, "skipped_uid", slide.UID, "image_slug", imageSlug)
			continue
		}
		mapping[cat.Hashtag] = slide.UID
		r.log.Info("poster auto-mapped",
			"hashtag", cat.Hashtag, "slide_uid", slide.UID, "image_id", imageID, "image_slug", imageSlug)
	}
}

// maybeUpdateHomepagePoster repoints the matching homepage poster's link at the freshly-published post; non-fatal on failure.
func (r *runner) maybeUpdateHomepagePoster(ctx context.Context, routed router.Routed, postID int) {
	if r.home == nil || len(r.cfg.Homepage.PosterLinks) == 0 || r.cfg.Homepage.PageID <= 0 {
		return
	}
	uid, ok := r.cfg.Homepage.PosterLinks[routed.Category]
	if !ok {
		// Fall back to matching by hashtag in case POSTER_LINKS was keyed off the hashtag, not the slug.
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

// markSeenAndPersist marks the message as seen and flushes the state file; save failures are logged loudly.
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
	r.warnIfSilentFetch(msgs)
	r.syncEditsAndDeletes(ctx, msgs)
	// State is persisted per-message inside processOne; no end-of-tick save needed.
	if n := r.processBatch(ctx, msgs); n == 0 {
		r.log.Info("idle", "msg", "no new messages")
	}
}

// warnIfSilentFetch surfaces a likely Eitaa markup change: a successful fetch
// that parsed zero messages while the seen-set for this channel is non-empty.
// Without this warning a parser break is invisible — healthcheck stays green
// and the bot quietly stops publishing.
func (r *runner) warnIfSilentFetch(msgs []eitaa.Message) {
	if len(msgs) > 0 {
		return
	}
	seen := r.store.Count(r.cfg.Source.Channel)
	if seen == 0 {
		// Legitimate empty state — first run on a fresh channel.
		return
	}
	r.log.Warn("fetch returned zero messages while seen-set is non-empty — Eitaa markup may have changed",
		"channel", r.cfg.Source.Channel,
		"seen_count", seen)
}

// syncEditsAndDeletes mirrors author edits/deletes from the source channel to WP for tracked messages still on the fetched page.
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

	// Upper bound is unbounded so a deleted "latest" message (missing from the page but ID > min) is still considered.
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

// maybeSyncEdit Updates the target when the fetched fingerprint differs; stored FP advances only on success.
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
