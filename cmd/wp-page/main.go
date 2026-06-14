// Command wp-page fetches and edits WordPress pages via REST. Reads
// WP_URL / WP_USER / WP_APP_PASSWORD from .env (same loader as the bridge).
//
// inspect:
//
//	go run ./cmd/wp-page --list                  # list every page
//	go run ./cmd/wp-page --title نخست            # search by title substring
//	go run ./cmd/wp-page --slug home             # exact slug lookup
//	go run ./cmd/wp-page --id 42                 # by post id
//
// edit (default = dry-run, prints the patch; add --apply to POST):
//
//	go run ./cmd/wp-page --id 2 --edit add-hadith
//	go run ./cmd/wp-page --id 2 --edit add-hadith --apply
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
)

const backupDir = "data/wp-page-backups"

type page struct {
	ID     int    `json:"id"`
	Slug   string `json:"slug"`
	Status string `json:"status"`
	Link   string `json:"link"`
	Title  struct {
		Raw      string `json:"raw"`
		Rendered string `json:"rendered"`
	} `json:"title"`
	Content struct {
		Raw      string `json:"raw"`
		Rendered string `json:"rendered"`
	} `json:"content"`
}

func main() {
	fs := flag.NewFlagSet("wp-page", flag.ExitOnError)
	envPath := fs.String("env", ".env", "path to .env file")
	list := fs.Bool("list", false, "list every page (id, slug, title)")
	title := fs.String("title", "", "search by title substring")
	slug := fs.String("slug", "", "exact slug lookup")
	id := fs.Int("id", 0, "lookup by post id")

	edit := fs.String("edit", "", "edit operation: add-hadith")
	apply := fs.Bool("apply", false, "actually POST the edit (default = dry-run)")
	revert := fs.Bool("revert", false, "restore --id from the most recent backup under data/wp-page-backups/")
	viaHelper := fs.Bool("via-helper", false, "for --edit add-hadith: call the eitaa-bridge-helper plugin endpoint instead of editing post_content directly (required for Enfold/Avia builder pages)")
	diagMeta := fs.Bool("diag-meta", false, "GET /eitaa-bridge/v1/page-meta?page_id=N (requires the helper plugin v1.2+)")
	diagKey := fs.String("diag-key", "", "if set with --diag-meta, fetch only this meta key (no truncation)")
	removeUID := fs.String("remove-slide-uid", "", "POST /eitaa-bridge/v1/remove-slide?page_id=N&uid=X (requires the helper plugin v1.3+)")
	hTitle := fs.String("hadith-title", "امام صادق علیه السلام :", "slide title (narrator)")
	hArabic := fs.String("hadith-arabic", "الصَّدَقَةُ تَدْفَعُ مِيتَةَ السُّوءِ", "Arabic text")
	hPersian := fs.String("hadith-persian", "صدقه دادن، مرگ بد را دفع می‌کند.", "Persian translation")
	hSource := fs.String("hadith-source", "الکافی ، ج ۴ ، ص ۶ ، ح ۵", "source citation")

	_ = fs.Parse(os.Args[1:])

	cfg := config.MustLoad(*envPath)
	cli := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	base := strings.TrimRight(cfg.WordPress.URL, "/")

	switch {
	case *removeUID != "":
		if *id <= 0 {
			die(fmt.Errorf("--remove-slide-uid requires --id"))
		}
		if err := runRemoveSlide(cli, base, cfg, *id, *removeUID, *apply); err != nil {
			die(err)
		}
	case *diagMeta:
		if *id <= 0 {
			die(fmt.Errorf("--diag-meta requires --id"))
		}
		if err := runDiagMeta(cli, base, cfg, *id, *diagKey); err != nil {
			die(err)
		}
	case *revert:
		if *id <= 0 {
			die(fmt.Errorf("--revert requires --id"))
		}
		if err := runRevert(cli, base, cfg, *id, *apply); err != nil {
			die(err)
		}
	case *edit != "":
		if *id <= 0 {
			die(fmt.Errorf("--edit requires --id"))
		}
		h := hadithInput{Title: *hTitle, Arabic: *hArabic, Persian: *hPersian, Source: *hSource}
		if *viaHelper {
			if err := runEditViaHelper(cli, base, cfg, *id, *edit, *apply, h); err != nil {
				die(err)
			}
		} else {
			if err := runEdit(cli, base, cfg, *id, *edit, *apply, h); err != nil {
				die(err)
			}
		}
	case *list:
		if err := listPages(cli, base, cfg); err != nil {
			die(err)
		}
	case *id > 0:
		p, err := getByID(cli, base, cfg, *id)
		if err != nil {
			die(err)
		}
		printPage(p)
	case *slug != "" || *title != "":
		pages, err := search(cli, base, cfg, *slug, *title)
		if err != nil {
			die(err)
		}
		if len(pages) == 0 {
			fmt.Println("no pages matched")
			return
		}
		for _, p := range pages {
			printPage(p)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: wp-page --list | --title TXT | --slug SLUG | --id N | --edit OP --id N")
		os.Exit(2)
	}
}

type hadithInput struct {
	Title, Arabic, Persian, Source string
}

func runEdit(cli *http.Client, base string, cfg *config.Config, id int, op string, apply bool, h hadithInput) error {
	p, err := getByID(cli, base, cfg, id)
	if err != nil {
		return fmt.Errorf("fetch page %d: %w", id, err)
	}

	var newContent string
	switch op {
	case "add-hadith":
		newContent, err = addHadith(p.Content.Raw, h)
	default:
		return fmt.Errorf("unknown edit op %q (supported: add-hadith)", op)
	}
	if err != nil {
		return err
	}

	bar := strings.Repeat("-", 80)
	fmt.Println(bar)
	fmt.Printf("EDIT  page %d (%s)\n", p.ID, p.Title.Raw)
	fmt.Printf("OP    %s\n", op)
	fmt.Println(bar)
	fmt.Println(diffSummary(p.Content.Raw, newContent))
	fmt.Println(bar)

	if !apply {
		fmt.Println("DRY-RUN — re-run with --apply to POST this change.")
		return nil
	}

	backupPath, err := saveBackup(id, p.Content.Raw)
	if err != nil {
		return fmt.Errorf("save backup: %w", err)
	}
	fmt.Printf("backup written: %s\n", backupPath)

	if err := updatePage(cli, base, cfg, id, newContent); err != nil {
		return fmt.Errorf("POST update: %w", err)
	}
	fmt.Printf("applied: page %d updated on %s\n", id, base)
	fmt.Printf("revert with: go run ./cmd/wp-page --id %d --revert --apply\n", id)
	return nil
}

// runDiagMeta hits the helper plugin's /page-meta endpoint and dumps every
// post_meta key/value so we can see where the theme stores its layout.
// When key != "", returns just that key's value with no truncation.
func runDiagMeta(cli *http.Client, base string, cfg *config.Config, id int, key string) error {
	u := fmt.Sprintf("%s/wp-json/eitaa-bridge/v1/page-meta?page_id=%d", base, id)
	if key != "" {
		u += "&key=" + url.QueryEscape(key)
	}
	body, err := get(cli, u, cfg)
	if err != nil {
		return err
	}
	var pretty map[string]any
	if err := json.Unmarshal(body, &pretty); err != nil {
		return fmt.Errorf("decode meta dump: %w (body: %s)", err, snippet(body))
	}
	out, _ := json.MarshalIndent(pretty, "", "  ")
	fmt.Println(string(out))
	return nil
}

// runRemoveSlide calls the helper plugin's /remove-slide endpoint, which
// strips an av_content_slide by its av_uid from BOTH _aviaLayoutBuilderCleanData
// and post_content. Default = dry-run.
func runRemoveSlide(cli *http.Client, base string, cfg *config.Config, id int, uid string, apply bool) error {
	bar := strings.Repeat("-", 80)
	fmt.Println(bar)
	fmt.Printf("REMOVE  page %d  slide uid=%s\n", id, uid)
	fmt.Println(bar)
	if !apply {
		fmt.Println("DRY-RUN — re-run with --apply to call /remove-slide.")
		return nil
	}
	payload, _ := json.Marshal(map[string]any{"page_id": id, "uid": uid})
	u := base + "/wp-json/eitaa-bridge/v1/remove-slide"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.SetBasicAuth(cfg.WordPress.Username, cfg.WordPress.AppPassword)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "wp-page-edit/1.0")
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("helper endpoint HTTP %d: %s", resp.StatusCode, snippet(respBody))
	}
	var pretty map[string]any
	_ = json.Unmarshal(respBody, &pretty)
	out, _ := json.MarshalIndent(pretty, "", "  ")
	fmt.Println(string(out))
	return nil
}

// runEditViaHelper sends the hadith fields to the plugin endpoint so the edit
// runs as PHP server-side (triggers save_post hooks, can bust Avia caches).
func runEditViaHelper(cli *http.Client, base string, cfg *config.Config, id int, op string, apply bool, h hadithInput) error {
	if op != "add-hadith" {
		return fmt.Errorf("--via-helper only supports --edit add-hadith")
	}

	bar := strings.Repeat("-", 80)
	fmt.Println(bar)
	fmt.Printf("EDIT (via helper plugin)  page %d\n", id)
	fmt.Printf("OP    %s\n", op)
	fmt.Println(bar)
	fmt.Printf("title   : %s\n", h.Title)
	fmt.Printf("arabic  : %s\n", h.Arabic)
	fmt.Printf("persian : %s\n", h.Persian)
	fmt.Printf("source  : %s\n", h.Source)
	fmt.Println(bar)

	if !apply {
		fmt.Println("DRY-RUN — re-run with --apply to call the plugin endpoint.")
		return nil
	}

	// Save a backup of current content first.
	page, err := getByID(cli, base, cfg, id)
	if err != nil {
		return fmt.Errorf("fetch page for backup: %w", err)
	}
	backupPath, err := saveBackup(id, page.Content.Raw)
	if err != nil {
		return fmt.Errorf("save backup: %w", err)
	}
	fmt.Printf("backup written: %s\n", backupPath)

	payload, _ := json.Marshal(map[string]any{
		"page_id": id,
		"title":   h.Title,
		"arabic":  h.Arabic,
		"persian": h.Persian,
		"source":  h.Source,
	})
	u := base + "/wp-json/eitaa-bridge/v1/add-hadith-slide"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.SetBasicAuth(cfg.WordPress.Username, cfg.WordPress.AppPassword)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "wp-page-edit/1.0")
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("helper endpoint HTTP %d: %s", resp.StatusCode, snippet(respBody))
	}
	var pretty map[string]any
	_ = json.Unmarshal(respBody, &pretty)
	out, _ := json.MarshalIndent(pretty, "", "  ")
	fmt.Println("plugin response:")
	fmt.Println(string(out))
	fmt.Printf("revert with: go run ./cmd/wp-page --id %d --revert --apply\n", id)
	return nil
}

// runRevert restores --id from the most recent backup file. Default = dry-run
// (shows what would be restored); --apply actually POSTs the rollback.
func runRevert(cli *http.Client, base string, cfg *config.Config, id int, apply bool) error {
	path, err := latestBackup(id)
	if err != nil {
		return err
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read backup %s: %w", path, err)
	}
	current, err := getByID(cli, base, cfg, id)
	if err != nil {
		return fmt.Errorf("fetch current page: %w", err)
	}

	bar := strings.Repeat("-", 80)
	fmt.Println(bar)
	fmt.Printf("REVERT  page %d (%s)\n", current.ID, current.Title.Raw)
	fmt.Printf("FROM    %s\n", path)
	fmt.Println(bar)
	if string(saved) == current.Content.Raw {
		fmt.Println("(backup and current are identical — nothing to revert)")
		return nil
	}
	fmt.Println(diffSummary(current.Content.Raw, string(saved)))
	fmt.Println(bar)

	if !apply {
		fmt.Println("DRY-RUN — re-run with --apply to POST the rollback.")
		return nil
	}
	if err := updatePage(cli, base, cfg, id, string(saved)); err != nil {
		return fmt.Errorf("POST update: %w", err)
	}
	fmt.Printf("reverted: page %d restored from %s\n", id, path)
	return nil
}

// saveBackup writes the current raw content to data/wp-page-backups/page-<id>-<ts>.raw.
func saveBackup(id int, raw string) (string, error) {
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("page-%d-%s.raw", id, time.Now().UTC().Format("20060102-150405"))
	path := filepath.Join(backupDir, name)
	return path, os.WriteFile(path, []byte(raw), 0o644)
}

// latestBackup returns the most recent backup file for the given page id.
func latestBackup(id int) (string, error) {
	prefix := fmt.Sprintf("page-%d-", id)
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return "", fmt.Errorf("read backup dir %s: %w", backupDir, err)
	}
	var matches []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			matches = append(matches, e.Name())
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no backup found for page %d under %s", id, backupDir)
	}
	sort.Strings(matches)
	return filepath.Join(backupDir, matches[len(matches)-1]), nil
}

// addHadith inserts a new av_content_slide immediately before [/av_content_slider].
// Format mirrors the existing slides on fatemyoon.ir/home.
func addHadith(content string, h hadithInput) (string, error) {
	const closer = "[/av_content_slider]"
	idx := strings.Index(content, closer)
	if idx < 0 {
		return "", fmt.Errorf("no [av_content_slider] block found on this page")
	}
	uid := randUID()
	slide := fmt.Sprintf(
		"[av_content_slide title='%s' heading_tag='' heading_class='' link='' linktarget='' av_uid='%s']\n"+
			"<p style=\"text-align: center;\"><strong>%s</strong>\n%s\n%s</p>\n"+
			"[/av_content_slide]\n",
		h.Title, uid, h.Arabic, h.Persian, h.Source)
	return content[:idx] + slide + content[idx:], nil
}

func updatePage(cli *http.Client, base string, cfg *config.Config, id int, content string) error {
	body, _ := json.Marshal(map[string]any{"content": content})
	u := fmt.Sprintf("%s/wp-json/wp/v2/pages/%d", base, id)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.SetBasicAuth(cfg.WordPress.Username, cfg.WordPress.AppPassword)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "wp-page-edit/1.0")
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(respBody))
	}
	return nil
}

// diffSummary picks the first run of changed/inserted lines and prints a small
// "before/after" window so the user can sanity-check the edit.
func diffSummary(before, after string) string {
	b := strings.Split(before, "\n")
	a := strings.Split(after, "\n")
	i := 0
	for i < len(b) && i < len(a) && b[i] == a[i] {
		i++
	}
	if i == len(b) && i == len(a) {
		return "(no changes)"
	}
	end := i + 12
	if end > len(a) {
		end = len(a)
	}
	ctx := i - 2
	if ctx < 0 {
		ctx = 0
	}
	var out strings.Builder
	out.WriteString("inserted around line ")
	out.WriteString(itoa(i + 1))
	out.WriteString(":\n\n")
	for k := ctx; k < end; k++ {
		marker := "  "
		if k >= i && k < i+(len(a)-len(b)) {
			marker = "+ "
		}
		out.WriteString(marker)
		out.WriteString(a[k])
		out.WriteString("\n")
	}
	return out.String()
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// randUID returns "av-" + 4 hex chars, matching the style of existing Avia uids.
func randUID() string {
	var buf [2]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("av-%x", buf)
}

func listPages(cli *http.Client, base string, cfg *config.Config) error {
	q := url.Values{}
	q.Set("status", "any")
	q.Set("per_page", "100")
	q.Set("orderby", "title")
	q.Set("order", "asc")
	q.Set("context", "edit")
	pages, err := getMany(cli, base+"/wp-json/wp/v2/pages?"+q.Encode(), cfg)
	if err != nil {
		return err
	}
	fmt.Printf("%-6s  %-30s  %s\n", "ID", "SLUG", "TITLE")
	fmt.Println(strings.Repeat("-", 80))
	for _, p := range pages {
		t := p.Title.Raw
		if t == "" {
			t = p.Title.Rendered
		}
		fmt.Printf("%-6d  %-30s  %s\n", p.ID, p.Slug, t)
	}
	return nil
}

func search(cli *http.Client, base string, cfg *config.Config, slug, title string) ([]page, error) {
	q := url.Values{}
	q.Set("status", "any")
	q.Set("per_page", "20")
	q.Set("context", "edit")
	if slug != "" {
		q.Set("slug", slug)
	}
	if title != "" {
		q.Set("search", title)
	}
	return getMany(cli, base+"/wp-json/wp/v2/pages?"+q.Encode(), cfg)
}

func getByID(cli *http.Client, base string, cfg *config.Config, id int) (page, error) {
	u := fmt.Sprintf("%s/wp-json/wp/v2/pages/%d?context=edit", base, id)
	body, err := get(cli, u, cfg)
	if err != nil {
		return page{}, err
	}
	var p page
	if err := json.Unmarshal(body, &p); err != nil {
		return page{}, fmt.Errorf("decode page %d: %w (body: %s)", id, err, snippet(body))
	}
	return p, nil
}

func getMany(cli *http.Client, u string, cfg *config.Config) ([]page, error) {
	body, err := get(cli, u, cfg)
	if err != nil {
		return nil, err
	}
	var pages []page
	if err := json.Unmarshal(body, &pages); err != nil {
		return nil, fmt.Errorf("decode pages: %w (body: %s)", err, snippet(body))
	}
	return pages, nil
}

func get(cli *http.Client, u string, cfg *config.Config) ([]byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(cfg.WordPress.Username, cfg.WordPress.AppPassword)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "wp-page-inspect/1.0")
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: HTTP %d: %s", u, resp.StatusCode, snippet(body))
	}
	return body, nil
}

func printPage(p page) {
	bar := strings.Repeat("=", 80)
	fmt.Println(bar)
	fmt.Printf("ID:     %d\n", p.ID)
	fmt.Printf("Slug:   %s\n", p.Slug)
	fmt.Printf("Status: %s\n", p.Status)
	fmt.Printf("Link:   %s\n", p.Link)
	fmt.Printf("Title:  %s\n", strings.TrimSpace(p.Title.Raw))
	fmt.Println(strings.Repeat("-", 80))
	fmt.Println("CONTENT (raw — shortcodes / theme markup):")
	fmt.Println()
	fmt.Println(p.Content.Raw)
	fmt.Println(bar)
}

func snippet(b []byte) string {
	const max = 500
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
