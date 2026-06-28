package eitaa

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// channelPage renders a minimal Eitaa-style channel page with one message
// containing a relative photo URL. It mimics just enough of the real markup
// to exercise Parse, parseMessage, and absURL.
func channelPage(channel string, id int) string {
	return fmt.Sprintf(`<!doctype html><html><body>
<div class="etme_widget_message" data-post="%s/%d">
  <a class="etme_widget_message_photo_wrap" style="background-image:url('/file/abc.jpg')"></a>
  <div class="etme_widget_message_text">hello</div>
  <time class="time" datetime="2026-01-01T00:00:00Z"></time>
</div>
</body></html>`, channel, id)
}

// TestParseUsesClientBaseURL covers issue #4: when a caller points Client.BaseURL
// at a test server, the resulting Message.Link and Photos must reference that
// server — not the hard-coded eitaa.com default.
func TestParseUsesClientBaseURL(t *testing.T) {
	const channel = "test_channel"
	const msgID = 42

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, channelPage(channel, msgID))
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL

	msgs, err := c.Fetch(context.Background(), channel)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}

	wantLink := fmt.Sprintf("%s/%s/%d", srv.URL, channel, msgID)
	if msgs[0].Link != wantLink {
		t.Errorf("Link = %q, want %q", msgs[0].Link, wantLink)
	}

	if len(msgs[0].Photos) != 1 {
		t.Fatalf("got %d photos, want 1", len(msgs[0].Photos))
	}
	wantPhoto := srv.URL + "/file/abc.jpg"
	if msgs[0].Photos[0] != wantPhoto {
		t.Errorf("Photo = %q, want %q", msgs[0].Photos[0], wantPhoto)
	}

	// Defensive: the real host must not leak into either field.
	for _, got := range []string{msgs[0].Link, msgs[0].Photos[0]} {
		if strings.Contains(got, "eitaa.com") {
			t.Errorf("output leaked default base URL: %q", got)
		}
	}
}

// TestParseDirectThreadsBaseURL exercises the exported Parse function (used
// by cmd/bridge/dump.go) to confirm the baseURL parameter flows through.
func TestParseDirectThreadsBaseURL(t *testing.T) {
	const channel = "ch"
	const base = "https://staging.example"

	msgs, err := Parse(base, channel, channelPage(channel, 7))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if want := base + "/ch/7"; msgs[0].Link != want {
		t.Errorf("Link = %q, want %q", msgs[0].Link, want)
	}
	if want := base + "/file/abc.jpg"; msgs[0].Photos[0] != want {
		t.Errorf("Photo = %q, want %q", msgs[0].Photos[0], want)
	}
}

// TestParseNormalizesBaseURL pins down two ergonomics rules: a trailing slash
// on baseURL must not produce a double slash in output, and an empty baseURL
// must fall back to the default rather than emitting bare "/ch/<id>" links.
func TestParseNormalizesBaseURL(t *testing.T) {
	const channel = "ch"
	const msgID = 9

	t.Run("trailing slash is trimmed", func(t *testing.T) {
		msgs, err := Parse("https://staging.example/", channel, channelPage(channel, msgID))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(msgs) != 1 {
			t.Fatalf("got %d messages, want 1", len(msgs))
		}
		if want := "https://staging.example/ch/9"; msgs[0].Link != want {
			t.Errorf("Link = %q, want %q", msgs[0].Link, want)
		}
		if want := "https://staging.example/file/abc.jpg"; msgs[0].Photos[0] != want {
			t.Errorf("Photo = %q, want %q", msgs[0].Photos[0], want)
		}
		// Belt-and-suspenders: no "//" allowed after the scheme separator.
		for _, got := range []string{msgs[0].Link, msgs[0].Photos[0]} {
			rest := strings.TrimPrefix(strings.TrimPrefix(got, "https://"), "http://")
			if strings.Contains(rest, "//") {
				t.Errorf("output has double slash: %q", got)
			}
		}
	})

	t.Run("empty baseURL falls back to default", func(t *testing.T) {
		msgs, err := Parse("", channel, channelPage(channel, msgID))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(msgs) != 1 {
			t.Fatalf("got %d messages, want 1", len(msgs))
		}
		if !strings.HasPrefix(msgs[0].Link, defaultBaseURL+"/") {
			t.Errorf("Link = %q, want it to start with %q", msgs[0].Link, defaultBaseURL+"/")
		}
		if !strings.HasPrefix(msgs[0].Photos[0], defaultBaseURL+"/") {
			t.Errorf("Photo = %q, want it to start with %q", msgs[0].Photos[0], defaultBaseURL+"/")
		}
	})
}
