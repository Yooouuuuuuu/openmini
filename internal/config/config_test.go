package config

import (
	"os"
	"path/filepath"
	"testing"
)

// write drops content into a temp config file and returns its path.
func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtraAntiTruncation(t *testing.T) {
	// [extra] anti_truncation turns it on; retries defaults to 1.
	c, err := Load(write(t, "[extra]\nanti_truncation = true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.AgyAPI.AntiTruncation {
		t.Errorf("anti_truncation from [extra] not applied")
	}
	if c.AgyAPI.Retries != 1 {
		t.Errorf("retries default = %d, want 1", c.AgyAPI.Retries)
	}
}

func TestAgyapiBackCompat(t *testing.T) {
	// The pre-0.6.2 location, [agyapi], is still honored when [extra] is silent.
	c, err := Load(write(t, "[agyapi]\nretries = 3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.AgyAPI.Retries != 3 {
		t.Errorf("retries from [agyapi] = %d, want 3", c.AgyAPI.Retries)
	}
}

func TestExtraOverridesAgyapi(t *testing.T) {
	// When both are set, [extra] wins.
	c, err := Load(write(t, "[agyapi]\nretries = 3\n[extra]\nretries = 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.AgyAPI.Retries != 5 {
		t.Errorf("retries = %d, want 5 ([extra] should win)", c.AgyAPI.Retries)
	}
}
