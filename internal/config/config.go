package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/themartz90/folderhug/internal/downloads"
	"github.com/themartz90/folderhug/internal/upstream"
)

type Config struct {
	Address              string
	PublicURL            *url.URL
	DataDir              string
	KeysDir              string
	DownloadsDir         string
	MountedRoots         []downloads.MountedRoot
	CacheDir             string
	UpstreamPolicy       upstream.Policy
	StreamMaxBytes       int64
	StreamTimeout        time.Duration
	DownloadBudgetBytes  int64
	DownloadReserveBytes int64
	DownloadUnknownBytes int64
	ThumbnailCacheBytes  int64
	MediaSocket          string
	WorkerKeyFile        string
}

func Load() (Config, error) {
	base, address := ".local", "127.0.0.1:8080"
	// The image supplies defaults without shadowing existing configuration aliases.
	if os.Getenv("FOLDERHUG_CONTAINER") == "1" {
		base, address = "/", "0.0.0.0:8080"
	}
	var u *url.URL
	if raw := Env("PUBLIC_URL"); raw != "" {
		var err error
		u, err = url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
			return Config{}, fmt.Errorf("FOLDERHUG_PUBLIC_URL must be an HTTP(S) origin without credentials, path, query, or fragment")
		}
		u.Path = ""
	}
	policy, err := loadAllowances(Env("UPSTREAM_ALLOW_FILE"))
	if err != nil {
		return Config{}, err
	}
	streamBytes, err := strconv.ParseInt(Value("STREAM_MAX_BYTES", "1099511627776"), 10, 64)
	if err != nil || streamBytes < 1 {
		return Config{}, fmt.Errorf("FOLDERHUG_STREAM_MAX_BYTES must be a positive byte count")
	}
	streamTimeout, err := time.ParseDuration(Value("STREAM_TIMEOUT", "24h"))
	if err != nil || streamTimeout < time.Second || streamTimeout > 7*24*time.Hour {
		return Config{}, fmt.Errorf("FOLDERHUG_STREAM_TIMEOUT must be between 1s and 168h")
	}
	limits := map[string]int64{}
	var mounted []downloads.MountedRoot
	if raw := Env("MOUNTED_ROOTS"); raw != "" {
		if len(raw) > 65536 || json.Unmarshal([]byte(raw), &mounted) != nil || len(mounted) > 32 {
			return Config{}, fmt.Errorf("FOLDERHUG_MOUNTED_ROOTS must be a JSON array of up to 32 roots")
		}
		seen := map[string]bool{"server": true}
		for _, root := range mounted {
			if root.ID == "" || len(root.ID) > 64 || seen[root.ID] || root.Label == "" || len(root.Label) > 120 || !filepath.IsAbs(root.Path) || filepath.Clean(root.Path) != root.Path {
				return Config{}, fmt.Errorf("FOLDERHUG_MOUNTED_ROOTS contains an invalid root")
			}
			for _, ch := range root.ID {
				if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
					return Config{}, fmt.Errorf("FOLDERHUG_MOUNTED_ROOTS contains an invalid root ID")
				}
			}
			seen[root.ID] = true
		}
	}
	for key, fallback := range map[string]string{"DOWNLOAD_BUDGET_BYTES": "107374182400", "DOWNLOAD_RESERVE_BYTES": "5368709120", "DOWNLOAD_UNKNOWN_BYTES": "21474836480", "THUMBNAIL_CACHE_BYTES": "1073741824"} {
		n, e := strconv.ParseInt(Value(key, fallback), 10, 64)
		if e != nil || n < 0 || (n == 0 && key != "DOWNLOAD_RESERVE_BYTES") {
			return Config{}, fmt.Errorf("FOLDERHUG_%s must be a valid byte count", key)
		}
		limits[key] = n
	}
	return Config{
		DownloadBudgetBytes: limits["DOWNLOAD_BUDGET_BYTES"], DownloadReserveBytes: limits["DOWNLOAD_RESERVE_BYTES"], DownloadUnknownBytes: limits["DOWNLOAD_UNKNOWN_BYTES"], ThumbnailCacheBytes: limits["THUMBNAIL_CACHE_BYTES"],
		MediaSocket: Env("MEDIA_SOCKET"), WorkerKeyFile: Value("WORKER_KEY", filepath.Join(base, "worker-key", "worker.key")),
		Address: Value("ADDR", address), PublicURL: u,
		DataDir:        Value("DATA_DIR", filepath.Join(base, "data")),
		KeysDir:        Value("KEYS_DIR", filepath.Join(base, "keys")),
		DownloadsDir:   Value("DOWNLOADS_DIR", filepath.Join(base, "downloads")),
		MountedRoots:   mounted,
		CacheDir:       Value("CACHE_DIR", filepath.Join(base, "cache")),
		UpstreamPolicy: policy,
		StreamMaxBytes: streamBytes, StreamTimeout: streamTimeout,
	}, nil
}

func loadAllowances(path string) (upstream.Policy, error) {
	var policy upstream.Policy
	if path == "" {
		return policy, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return policy, fmt.Errorf("cannot read upstream allowance file")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(b) > 65536 {
		return policy, fmt.Errorf("upstream allowance file exceeds 64 KiB")
	}
	var rules []struct {
		Host  string   `json:"host"`
		Port  uint16   `json:"port"`
		CIDRs []string `json:"cidrs"`
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&rules) != nil || d.Decode(new(any)) != io.EOF || len(rules) > 128 {
		return policy, fmt.Errorf("invalid upstream allowance file")
	}
	for _, rule := range rules {
		if rule.Host == "" || rule.Port == 0 || len(rule.CIDRs) == 0 || strings.ContainsAny(rule.Host, "/\\@?# \t\r\n") {
			return policy, fmt.Errorf("invalid upstream allowance rule")
		}
		allow := upstream.Allowance{Host: strings.ToLower(strings.TrimSuffix(rule.Host, ".")), Port: rule.Port}
		for _, raw := range rule.CIDRs {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				return policy, fmt.Errorf("invalid upstream allowance CIDR")
			}
			allow.CIDRs = append(allow.CIDRs, prefix.Masked())
		}
		policy.Allow = append(policy.Allow, allow)
	}
	return policy, nil
}

// Env prefers the Folderhug setting and accepts the original name for existing installations.
func Env(suffix string) string {
	if v, present := os.LookupEnv("FOLDERHUG_" + suffix); present {
		return v
	}
	return os.Getenv("HTTPWRAPPER_" + suffix)
}

func Value(suffix, fallback string) string {
	if v := Env(suffix); v != "" {
		return v
	}
	return fallback
}
