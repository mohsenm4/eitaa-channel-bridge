package config

import "testing"

// setRequired fills the env vars Load refuses to run without.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("EITAA_CHANNEL", "test")
	t.Setenv("WP_URL", "https://example.com")
	t.Setenv("WP_USER", "bot")
	t.Setenv("WP_APP_PASSWORD", "not-a-real-password")
}

func TestLoad_SyncBrakeDefaults(t *testing.T) {
	setRequired(t)
	t.Setenv("SYNC_MAX_DELETES", "")
	t.Setenv("SYNC_MAX_EDITS", "")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source.SyncMaxDeletes != 3 || cfg.Source.SyncMaxEdits != 5 {
		t.Errorf("defaults = %d deletes / %d edits, want 3 / 5", cfg.Source.SyncMaxDeletes, cfg.Source.SyncMaxEdits)
	}
}

func TestLoad_SyncBrakeCustomValues(t *testing.T) {
	setRequired(t)
	t.Setenv("SYNC_MAX_DELETES", "10")
	t.Setenv("SYNC_MAX_EDITS", "20")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source.SyncMaxDeletes != 10 || cfg.Source.SyncMaxEdits != 20 {
		t.Errorf("got %d deletes / %d edits, want 10 / 20", cfg.Source.SyncMaxDeletes, cfg.Source.SyncMaxEdits)
	}
}

func TestLoad_SyncBrakeRejectsBadValues(t *testing.T) {
	cases := []struct{ key, val string }{
		{"SYNC_MAX_DELETES", "-1"},
		{"SYNC_MAX_EDITS", "-2"},
		{"SYNC_MAX_DELETES", "many"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.val, func(t *testing.T) {
			setRequired(t)
			t.Setenv(tc.key, tc.val)
			if _, err := Load(""); err == nil {
				t.Errorf("expected an error for %s=%s", tc.key, tc.val)
			}
		})
	}
}
