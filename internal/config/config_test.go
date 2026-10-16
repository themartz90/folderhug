package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateChecksConfiguration(t *testing.T) {
	t.Setenv("FOLDERHUG_UPDATE_CHECKS", "")
	t.Setenv("HTTPWRAPPER_UPDATE_CHECKS", "false")
	cfg, err := Load()
	if err != nil || !cfg.UpdateChecks {
		t.Fatalf("default: %+v %v", cfg, err)
	}
	if err := os.Unsetenv("FOLDERHUG_UPDATE_CHECKS"); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil || cfg.UpdateChecks {
		t.Fatalf("legacy fallback: %+v %v", cfg, err)
	}
	t.Setenv("FOLDERHUG_UPDATE_CHECKS", "false")
	cfg, err = Load()
	if err != nil || cfg.UpdateChecks {
		t.Fatalf("disabled: %+v %v", cfg, err)
	}
	t.Setenv("FOLDERHUG_UPDATE_CHECKS", "true")
	cfg, err = Load()
	if err != nil || !cfg.UpdateChecks {
		t.Fatalf("enabled: %+v %v", cfg, err)
	}
	t.Setenv("FOLDERHUG_UPDATE_CHECKS", "perhaps")
	if _, err := Load(); err == nil {
		t.Fatal("invalid boolean accepted")
	}
}

func TestOriginAndAllowances(t *testing.T) {
	for _, raw := range []string{"https://example.com/path", "https://user@example.com", "file:///tmp", "https://example.com?q=x"} {
		t.Setenv("FOLDERHUG_PUBLIC_URL", raw)
		if _, err := Load(); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	t.Setenv("FOLDERHUG_PUBLIC_URL", "https://explorer.example.com/")
	file := filepath.Join(t.TempDir(), "allow.json")
	if err := os.WriteFile(file, []byte(`[{"host":"NAS.example.com","port":8080,"cidrs":["192.168.1.0/24"]}]`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOLDERHUG_UPSTREAM_ALLOW_FILE", file)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL.String() != "https://explorer.example.com" || cfg.UpstreamPolicy.Allow[0].Host != "nas.example.com" {
		t.Fatal("configuration normalization failed")
	}
	for _, body := range []string{`[{"host":"x","port":80,"cidrs":["bad"]}]`, `[{"host":"x","port":80,"cidrs":[],"extra":true}]`, `[] {}`} {
		if err = os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = Load(); err == nil {
			t.Fatal("invalid rule accepted")
		}
	}
}

func TestMountedRoots(t *testing.T) {
	path := t.TempDir()
	t.Setenv("FOLDERHUG_MOUNTED_ROOTS", fmt.Sprintf(`[{"id":"media","label":"Media","path":%q}]`, path))
	cfg, err := Load()
	if err != nil || len(cfg.MountedRoots) != 1 || cfg.MountedRoots[0].Path != path {
		t.Fatalf("mounted roots: %+v, %v", cfg.MountedRoots, err)
	}
	for _, raw := range []string{`[{"id":"server","label":"Bad","path":"/tmp"}]`, `[{"id":"media","label":"A","path":"relative"}]`, `[{"id":"Media","label":"A","path":"/tmp"}]`} {
		t.Setenv("FOLDERHUG_MOUNTED_ROOTS", raw)
		if _, err := Load(); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestStorageLimits(t *testing.T) {
	for _, key := range []string{"FOLDERHUG_DOWNLOAD_BUDGET_BYTES", "FOLDERHUG_DOWNLOAD_UNKNOWN_BYTES", "FOLDERHUG_THUMBNAIL_CACHE_BYTES"} {
		t.Run(key, func(t *testing.T) {
			for _, bad := range []string{"0", "-1", "1.5", "9223372036854775808"} {
				t.Setenv(key, bad)
				if _, err := Load(); err == nil {
					t.Fatalf("accepted %s", bad)
				}
			}
			t.Setenv(key, "1048576")
			if _, err := Load(); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Setenv("FOLDERHUG_DOWNLOAD_RESERVE_BYTES", "0")
	cfg, err := Load()
	if err != nil || cfg.DownloadReserveBytes != 0 {
		t.Fatal(err)
	}
	t.Setenv("FOLDERHUG_DOWNLOAD_RESERVE_BYTES", "-1")
	if _, err = Load(); err == nil {
		t.Fatal("negative reserve accepted")
	}
}

func TestFolderhugSettingsTakePrecedenceOverLegacySettings(t *testing.T) {
	t.Setenv("HTTPWRAPPER_PUBLIC_URL", "https://old.example.test")
	t.Setenv("FOLDERHUG_PUBLIC_URL", "https://folderhug.example.test")
	t.Setenv("HTTPWRAPPER_DATA_DIR", "old-data")
	t.Setenv("FOLDERHUG_DATA_DIR", "folderhug-data")
	t.Setenv("HTTPWRAPPER_DOWNLOAD_BUDGET_BYTES", "12345")
	t.Setenv("FOLDERHUG_DOWNLOAD_BUDGET_BYTES", "54321")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL.String() != "https://folderhug.example.test" || cfg.DataDir != "folderhug-data" || cfg.DownloadBudgetBytes != 54321 {
		t.Fatalf("Folderhug settings did not take precedence: %+v", cfg)
	}
	t.Setenv("FOLDERHUG_PUBLIC_URL", "")
	t.Setenv("FOLDERHUG_DATA_DIR", "")
	t.Setenv("FOLDERHUG_DOWNLOAD_BUDGET_BYTES", "")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != nil || cfg.DataDir != filepath.Join(".local", "data") || cfg.DownloadBudgetBytes != 107374182400 {
		t.Fatalf("empty Folderhug settings fell back to legacy values: %+v", cfg)
	}
	for _, key := range []string{"FOLDERHUG_PUBLIC_URL", "FOLDERHUG_DATA_DIR", "FOLDERHUG_DOWNLOAD_BUDGET_BYTES"} {
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL.String() != "https://old.example.test" || cfg.DataDir != "old-data" || cfg.DownloadBudgetBytes != 12345 {
		t.Fatalf("legacy settings stopped working: %+v", cfg)
	}
}

func TestFolderhugSettingValidation(t *testing.T) {
	t.Setenv("FOLDERHUG_PUBLIC_URL", "https://folderhug.example.test/path")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "FOLDERHUG_PUBLIC_URL") {
		t.Fatalf("expected canonical setting in error, got %v", err)
	}
}

func TestImageDefaultsPreserveLegacyOverrides(t *testing.T) {
	t.Setenv("FOLDERHUG_CONTAINER", "1")
	t.Setenv("FOLDERHUG_PUBLIC_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Address != "0.0.0.0:8080" || cfg.DataDir != filepath.Join("/", "data") || cfg.PublicURL != nil {
		t.Fatal("incorrect container defaults", cfg)
	}
	t.Setenv("HTTPWRAPPER_DATA_DIR", "existing-data")
	t.Setenv("HTTPWRAPPER_ADDR", "127.0.0.1:9090")
	cfg, err = Load()
	if err != nil || cfg.DataDir != "existing-data" || cfg.Address != "127.0.0.1:9090" {
		t.Fatal("image defaults hid legacy settings", cfg, err)
	}
}
