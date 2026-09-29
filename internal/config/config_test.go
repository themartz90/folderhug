package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestOriginAndAllowances(t *testing.T) {
	for _, raw := range []string{"https://example.com/path", "https://user@example.com", "file:///tmp", "https://example.com?q=x"} {
		t.Setenv("HTTPWRAPPER_PUBLIC_URL", raw)
		if _, err := Load(); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	t.Setenv("HTTPWRAPPER_PUBLIC_URL", "https://explorer.example.com/")
	file := filepath.Join(t.TempDir(), "allow.json")
	if err := os.WriteFile(file, []byte(`[{"host":"NAS.example.com","port":8080,"cidrs":["192.168.1.0/24"]}]`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTPWRAPPER_UPSTREAM_ALLOW_FILE", file)
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
	t.Setenv("HTTPWRAPPER_MOUNTED_ROOTS", fmt.Sprintf(`[{"id":"media","label":"Media","path":%q}]`, path))
	cfg, err := Load()
	if err != nil || len(cfg.MountedRoots) != 1 || cfg.MountedRoots[0].Path != path {
		t.Fatalf("mounted roots: %+v, %v", cfg.MountedRoots, err)
	}
	for _, raw := range []string{`[{"id":"server","label":"Bad","path":"/tmp"}]`, `[{"id":"media","label":"A","path":"relative"}]`, `[{"id":"Media","label":"A","path":"/tmp"}]`} {
		t.Setenv("HTTPWRAPPER_MOUNTED_ROOTS", raw)
		if _, err := Load(); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestStorageLimits(t *testing.T) {
	for _, key := range []string{"HTTPWRAPPER_DOWNLOAD_BUDGET_BYTES", "HTTPWRAPPER_DOWNLOAD_UNKNOWN_BYTES", "HTTPWRAPPER_THUMBNAIL_CACHE_BYTES"} {
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
	t.Setenv("HTTPWRAPPER_DOWNLOAD_RESERVE_BYTES", "0")
	cfg, err := Load()
	if err != nil || cfg.DownloadReserveBytes != 0 {
		t.Fatal(err)
	}
	t.Setenv("HTTPWRAPPER_DOWNLOAD_RESERVE_BYTES", "-1")
	if _, err = Load(); err == nil {
		t.Fatal("negative reserve accepted")
	}
}
