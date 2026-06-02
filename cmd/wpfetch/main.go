// wpfetch: fetch the N most recent posts from each target category
// to inspect their structure. Used to design a unified Eitaa-channel
// message template that matches existing site content.
package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// The three existing units from the stakeholder meeting. The 4th unit
// (رضوان) and the separate "گزارش جلسات واحدها" category haven't been
// created yet, so we sample only what exists.
var targetSlugs = []string{
	"واحد-قرض-الحسنه",
	"حمایت-و-خدمت",
	"واحد-توانمندسازی",
}

type category struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Count int    `json:"count"`
}

type renderedField struct {
	Rendered string `json:"rendered"`
}

type post struct {
	ID            int           `json:"id"`
	Date          string        `json:"date"`
	Link          string        `json:"link"`
	Status        string        `json:"status"`
	Title         renderedField `json:"title"`
	Excerpt       renderedField `json:"excerpt"`
	Content       renderedField `json:"content"`
	FeaturedMedia int           `json:"featured_media"`
}

var (
	baseURL string
	user    string
	pass    string
	client  *http.Client
)

func authedGET(path string) ([]byte, error) {
	req, _ := http.NewRequest("GET", baseURL+path, nil)
	req.SetBasicAuth(user, pass)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		preview := string(body)
		if len(preview) > 300 {
			preview = preview[:300]
		}
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, preview)
	}
	return body, nil
}

func findCategory(slug string) (*category, error) {
	v := url.Values{}
	v.Set("slug", slug)
	body, err := authedGET("/wp-json/wp/v2/categories?" + v.Encode())
	if err != nil {
		return nil, err
	}
	var cats []category
	if err := json.Unmarshal(body, &cats); err != nil {
		return nil, fmt.Errorf("decode categories: %w", err)
	}
	if len(cats) == 0 {
		return nil, fmt.Errorf("no category with slug %q", slug)
	}
	return &cats[0], nil
}

func fetchPosts(catID, n int) ([]post, error) {
	v := url.Values{}
	v.Set("categories", fmt.Sprint(catID))
	v.Set("per_page", fmt.Sprint(n))
	v.Set("orderby", "date")
	v.Set("order", "desc")
	body, err := authedGET("/wp-json/wp/v2/posts?" + v.Encode())
	if err != nil {
		return nil, err
	}
	var posts []post
	if err := json.Unmarshal(body, &posts); err != nil {
		return nil, fmt.Errorf("decode posts: %w", err)
	}
	return posts, nil
}

var (
	reScript = regexp.MustCompile(`(?s)<script[^>]*>.*?</script>`)
	reStyle  = regexp.MustCompile(`(?s)<style[^>]*>.*?</style>`)
	reBr     = regexp.MustCompile(`<br\s*/?>`)
	rePEnd   = regexp.MustCompile(`</p\s*>`)
	reTag    = regexp.MustCompile(`<[^>]+>`)
	reSpace  = regexp.MustCompile(`[\t ]+`)
	reNL     = regexp.MustCompile(`\n{3,}`)
	reImg    = regexp.MustCompile(`<img[^>]+src=["']([^"']+)["']`)
)

func stripHTML(s string) string {
	s = reScript.ReplaceAllString(s, "")
	s = reStyle.ReplaceAllString(s, "")
	s = reBr.ReplaceAllString(s, "\n")
	s = rePEnd.ReplaceAllString(s, "\n\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reSpace.ReplaceAllString(s, " ")
	s = reNL.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func extractImages(htmlSrc string) []string {
	matches := reImg.FindAllStringSubmatch(htmlSrc, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func main() {
	insecure := flag.Bool("insecure", false, "skip TLS cert verification")
	count := flag.Int("n", 5, "number of recent posts per category")
	outPath := flag.String("out", "data/wpfetch.txt", "output file path")
	flag.Parse()

	_ = godotenv.Load()
	baseURL = os.Getenv("EITAA_BRIDGE_TARGET_WORDPRESS_URL")
	user = os.Getenv("EITAA_BRIDGE_TARGET_WORDPRESS_USERNAME")
	pass = os.Getenv("EITAA_BRIDGE_TARGET_WORDPRESS_APP_PASSWORD")
	if baseURL == "" || user == "" || pass == "" {
		fmt.Fprintln(os.Stderr, "missing EITAA_BRIDGE_TARGET_WORDPRESS_{URL,USERNAME,APP_PASSWORD}")
		os.Exit(2)
	}

	tr := &http.Transport{}
	if *insecure {
		fmt.Fprintln(os.Stderr, "warning: TLS verification disabled (-insecure)")
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	client = &http.Client{Timeout: 30 * time.Second, Transport: tr}

	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir: %v\n", err)
		os.Exit(1)
	}
	f, err := os.Create(*outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create %s: %v\n", *outPath, err)
		os.Exit(1)
	}
	defer f.Close()
	out := f

	for _, slug := range targetSlugs {
		fmt.Fprintln(out, strings.Repeat("=", 78))
		fmt.Fprintf(out, "=== slug: %s\n", slug)
		cat, err := findCategory(slug)
		if err != nil {
			fmt.Fprintf(out, "    error: %v\n\n", err)
			continue
		}
		fmt.Fprintf(out, "=== name: %s   id=%d   total=%d\n", cat.Name, cat.ID, cat.Count)
		fmt.Fprintln(out, strings.Repeat("=", 78))

		posts, err := fetchPosts(cat.ID, *count)
		if err != nil {
			fmt.Fprintf(out, "    error: %v\n\n", err)
			continue
		}

		for i, p := range posts {
			date := p.Date
			if len(date) >= 10 {
				date = date[:10]
			}
			fmt.Fprintf(out, "\n--- [%d/%d]  id=%d  date=%s  status=%s ---\n",
				i+1, len(posts), p.ID, date, p.Status)
			fmt.Fprintf(out, "title:    %s\n", stripHTML(p.Title.Rendered))
			fmt.Fprintf(out, "link:     %s\n", p.Link)
			fmt.Fprintf(out, "featured_media: %d\n", p.FeaturedMedia)

			imgs := extractImages(p.Content.Rendered)
			fmt.Fprintf(out, "images in content: %d\n", len(imgs))
			for j, img := range imgs {
				if j >= 3 {
					fmt.Fprintf(out, "  ... (+%d more)\n", len(imgs)-3)
					break
				}
				fmt.Fprintf(out, "  - %s\n", img)
			}

			text := stripHTML(p.Content.Rendered)
			fmt.Fprintf(out, "content (cleaned):\n%s\n", truncRunes(text, 1000))
		}
		fmt.Fprintln(out)
	}

	fmt.Fprintf(os.Stderr, "wrote %s\n", *outPath)
}
