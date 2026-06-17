package main

import "testing"

func TestMatchInboxHashtag(t *testing.T) {
	cases := []struct {
		name   string
		inbox  []string
		tags   []string
		expect string
	}{
		{"empty inbox", nil, []string{"حدیث"}, ""},
		{"empty msg tags", []string{"حدیث"}, nil, ""},
		{"hit", []string{"حدیث"}, []string{"حدیث"}, "حدیث"},
		{"miss", []string{"حدیث"}, []string{"نوجوانان"}, ""},
		{"first hit wins on multi-tag msg",
			[]string{"حدیث", "نوجوانان"},
			[]string{"نوجوانان", "حدیث"},
			"نوجوانان"},
		{"inbox order doesn't matter",
			[]string{"x", "y", "حدیث"},
			[]string{"حدیث"},
			"حدیث"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := matchInboxHashtag(c.inbox, c.tags)
			if got != c.expect {
				t.Errorf("matchInboxHashtag(%v, %v) = %q, want %q",
					c.inbox, c.tags, got, c.expect)
			}
		})
	}
}
