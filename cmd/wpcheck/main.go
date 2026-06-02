// wpcheck: smoke test for WordPress REST API connectivity.
// Reads EITAA_BRIDGE_TARGET_WORDPRESS_{URL,USERNAME,APP_PASSWORD}
// from env or .env, then hits /wp-json/wp/v2/users/me with Basic
// auth. HTTP 200 means credentials work.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	insecure := flag.Bool("insecure", false, "skip TLS cert verification (use when site cert is expired)")
	flag.Parse()

	_ = godotenv.Load()

	base := os.Getenv("EITAA_BRIDGE_TARGET_WORDPRESS_URL")
	user := os.Getenv("EITAA_BRIDGE_TARGET_WORDPRESS_USERNAME")
	pass := os.Getenv("EITAA_BRIDGE_TARGET_WORDPRESS_APP_PASSWORD")
	if base == "" || user == "" || pass == "" {
		fmt.Fprintln(os.Stderr, "missing EITAA_BRIDGE_TARGET_WORDPRESS_{URL,USERNAME,APP_PASSWORD}")
		os.Exit(2)
	}

	tr := &http.Transport{}
	if *insecure {
		fmt.Fprintln(os.Stderr, "warning: TLS verification disabled (-insecure)")
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	client := &http.Client{Timeout: 20 * time.Second, Transport: tr}

	endpoints := []string{
		"/wp-json/wp/v2/users/me?context=edit",
		"/wp-json/wp/v2/categories?context=edit&per_page=1",
		"/wp-json/wp/v2/posts?status=draft&per_page=1",
	}

	for _, ep := range endpoints {
		url := base + ep
		fmt.Printf("============================================\nGET %s\n", url)

		req, _ := http.NewRequest("GET", url, nil)
		req.SetBasicAuth(user, pass)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "request failed: %v\n\n", err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		fmt.Printf("status: %s\n", resp.Status)
		for k, v := range resp.Header {
			fmt.Printf("  %s: %s\n", k, v)
		}
		fmt.Printf("body (first 400 chars):\n%s\n\n", trim(string(body), 400))
	}
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
