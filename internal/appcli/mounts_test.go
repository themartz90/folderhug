package appcli

import (
	"path/filepath"
	"testing"

	"github.com/themartz90/folderhug/internal/config"
	"github.com/themartz90/folderhug/internal/downloadfs"
	"github.com/themartz90/folderhug/internal/downloads"
)

func TestMergeDownloadMountsKeepsLegacyIdentity(t *testing.T) {
	explicit := []downloads.MountedRoot{{ID: "media", Label: "My files", Path: "/mnt/library"}, {ID: "archive", Label: "Archive", Path: "/elsewhere"}}
	discovered := []downloadfs.DiscoveredMount{{Path: "/mnt/library", Label: "library"}, {Path: "/mnt/second", Label: "second"}}
	roots, err := mergeDownloadMounts(explicit, discovered, []string{"/keys"})
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 3 || roots[0].ID != "media" || roots[0].Label != "My files" || !roots[0].GuardMount || roots[1].GuardMount {
		t.Fatal(roots)
	}
	if roots[2].ID != autoMountID("/mnt/second") || roots[2].Label != "second" || !roots[2].GuardMount || len(roots[2].ProtectedPaths) != 1 {
		t.Fatal(roots[2])
	}
	if roots[2].ID != autoMountID("/mnt/second") || roots[2].ID == autoMountID("/mnt/other") || len(roots[2].ID) > 64 {
		t.Fatal("unstable automatic ID")
	}
	if _, err := mergeDownloadMounts([]downloads.MountedRoot{{ID: autoMountID("/mnt/second"), Label: "Other", Path: "/old"}}, discovered, nil); err == nil {
		t.Fatal("allowed generated ID to point to another path")
	}
}

func TestProtectedMountPaths(t *testing.T) {
	cfg := config.Config{DataDir: "/data", KeysDir: "/keys", DownloadsDir: "/downloads", CacheDir: "/cache", WorkerKeyFile: "/worker-key/worker.key", MediaSocket: "/broker/broker.sock"}
	got := mountProtectedPaths(cfg)
	want := []string{"/data", "/keys", "/downloads", "/cache", filepath.Dir(cfg.WorkerKeyFile), filepath.Dir(cfg.MediaSocket)}
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatal(got, want)
		}
	}
}

func TestExplicitMntRootStaysGuardedWhenMissing(t *testing.T) {
	roots, err := mergeDownloadMounts([]downloads.MountedRoot{{ID: "drive", Label: "Drive", Path: "/mnt/drive"}, {ID: "native", Label: "Native", Path: "/home/media"}}, nil, []string{"/keys"})
	if err != nil {
		t.Fatal(err)
	}
	if !roots[0].GuardMount || len(roots[0].ProtectedPaths) != 1 || roots[1].GuardMount {
		t.Fatal(roots)
	}
}
