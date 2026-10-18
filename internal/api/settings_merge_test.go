package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func newTestSettings(t *testing.T) *SettingsCache {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "vaultdock.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c, err := LoadSettings(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSettingsMerge(t *testing.T) {
	ctx := context.Background()
	c := newTestSettings(t)
	base := defaultSettings()
	base.SiteName = "Base"
	base.MinFreeBytes = 5 << 30
	base.KeepReplacedVersions = true
	if err := c.Save(ctx, base); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		body   string
		code   string // expected apiError code, "" for success
		mutate func(*Settings)
	}{
		{name: "empty object keeps everything", body: `{}`, mutate: func(*Settings) {}},
		{name: "single field", body: `{"registration_enabled":true}`, mutate: func(s *Settings) { s.RegistrationEnabled = true }},
		{name: "explicit false applied", body: `{"keep_replaced_versions":false}`, mutate: func(s *Settings) { s.KeepReplacedVersions = false }},
		{name: "explicit zero applied", body: `{"min_free_bytes":0,"site_name":"New"}`, mutate: func(s *Settings) { s.MinFreeBytes = 0; s.SiteName = "New" }},
		{name: "unknown field rejected", body: `{"site_name":"X","bogus":1}`, code: "bad_json"},
		{name: "wrong type rejected", body: `{"trash_retention_days":"x"}`, code: "bad_json"},
		{name: "not an object", body: `null`, code: "bad_json"},
		{name: "array rejected", body: `[]`, code: "bad_json"},
		{name: "invalid value rejected", body: `{"activity_retention_days":1}`, code: "invalid_settings"},
		{name: "empty site name rejected", body: `{"site_name":"  "}`, code: "invalid_settings"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := c.Get()
			got, err := c.Merge(ctx, []byte(tc.body))
			if tc.code != "" {
				var ae *apiError
				if !errors.As(err, &ae) || ae.Code != tc.code || ae.Status != 400 {
					t.Fatalf("err = %v, want 400 %s", err, tc.code)
				}
				if c.Get() != before {
					t.Fatalf("settings changed on error: %+v", c.Get())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := before
			tc.mutate(&want)
			if got != want || c.Get() != want {
				t.Fatalf("got %+v, cache %+v, want %+v", got, c.Get(), want)
			}
		})
	}

	// The merged result is persisted, not only cached.
	reloaded, err := LoadSettings(ctx, c.st)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Get() != c.Get() {
		t.Fatalf("reloaded %+v, want %+v", reloaded.Get(), c.Get())
	}
}

func TestSettingsSaveValidationMessage(t *testing.T) {
	c := newTestSettings(t)
	v := defaultSettings()
	v.SiteName = ""
	err := c.Save(context.Background(), v)
	if err == nil || err.Error() != "site name must be 1-60 characters" {
		t.Fatalf("err = %v", err)
	}
}
