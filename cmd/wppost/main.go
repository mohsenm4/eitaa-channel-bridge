// wppost: end-to-end smoke test for WordPress publishing.
// Creates one draft post via REST API using credentials from .env.
// On success, prints the post id and edit URL so it can be reviewed
// in the WP admin panel before any real publisher work is committed.
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
)

type createPostRequest struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

type createPostResponse struct {
	ID     int    `json:"id"`
	Link   string `json:"link"`
	Status string `json:"status"`
	Title  struct {
		Rendered string `json:"rendered"`
	} `json:"title"`
}

func main() {
	insecure := flag.Bool("insecure", false, "skip TLS cert verification")
	flag.Parse()

	_ = godotenv.Load()
	base := os.Getenv("EITAA_BRIDGE_TARGET_WORDPRESS_URL")
	user := os.Getenv("EITAA_BRIDGE_TARGET_WORDPRESS_USERNAME")
	pass := os.Getenv("EITAA_BRIDGE_TARGET_WORDPRESS_APP_PASSWORD")
	if base == "" || user == "" || pass == "" {
		fmt.Fprintln(os.Stderr, "missing EITAA_BRIDGE_TARGET_WORDPRESS_{URL,USERNAME,APP_PASSWORD}")
		os.Exit(2)
	}

	payload := createPostRequest{
		Title: "[تست پل] پیام نمونه از ایتاع",
		Content: "<p>این یک پست تستی است که توسط <strong>wppost</strong> ایجاد شده تا اتصال بین ایتاع-بریج و وردپرس راستی‌آزمایی شود.</p>" +
			"<p>اگر این پست را در لیست draftهای پنل می‌بینی، یعنی publisher آماده‌ی پیاده‌سازی واقعی است.</p>" +
			"<p>زمان ایجاد: " + time.Now().Format(time.RFC3339) + "</p>",
		Status: "draft",
	}
	body, _ := json.Marshal(payload)

	url := base + "/wp-json/wp/v2/posts"
	fmt.Printf("POST %s\nas user: %s\nstatus: %s\n\n", url, user, payload.Status)

	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.SetBasicAuth(user, pass)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	tr := &http.Transport{}
	if *insecure {
		fmt.Fprintln(os.Stderr, "warning: TLS verification disabled (-insecure)")
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second, Transport: tr}).Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	fmt.Printf("response status: %s\n\n", resp.Status)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Println(string(respBody))
		os.Exit(1)
	}

	var out createPostResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		fmt.Println(string(respBody))
		os.Exit(1)
	}
	fmt.Printf("created draft:\n")
	fmt.Printf("  id:        %d\n", out.ID)
	fmt.Printf("  status:    %s\n", out.Status)
	fmt.Printf("  title:     %s\n", out.Title.Rendered)
	fmt.Printf("  preview:   %s\n", out.Link)
	fmt.Printf("  edit URL:  %s/wp-admin/post.php?post=%d&action=edit\n", base, out.ID)
}
