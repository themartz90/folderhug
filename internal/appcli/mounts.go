package appcli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"

	"github.com/themartz90/folderhug/internal/config"
	"github.com/themartz90/folderhug/internal/downloadfs"
	"github.com/themartz90/folderhug/internal/downloads"
)

func mountProtectedPaths(cfg config.Config) []string {
	return []string{cfg.DataDir, cfg.KeysDir, cfg.DownloadsDir, cfg.CacheDir}
}

func autoMountID(path string) string {
	digest := sha256.Sum256([]byte(path))
	return "mnt-" + hex.EncodeToString(digest[:28])
}

func mergeDownloadMounts(explicit []downloads.MountedRoot, discovered []downloadfs.DiscoveredMount, protected []string) ([]downloads.MountedRoot, error) {
	merged := append([]downloads.MountedRoot(nil), explicit...)
	byID := map[string]string{"server": ""}
	byPath := map[string]int{}
	for i, root := range merged {
		// A configured /mnt destination remains guarded when its bind mount is
		// absent at startup. Otherwise the underlying directory could be used.
		if path.IsAbs(root.Path) && path.Clean(root.Path) == root.Path && path.Dir(root.Path) == "/mnt" {
			merged[i].GuardMount = true
			merged[i].ProtectedPaths = append([]string(nil), protected...)
		}
		if prior, exists := byID[root.ID]; exists && prior != root.Path {
			return nil, fmt.Errorf("mounted destination ID is used by different paths")
		}
		if _, exists := byPath[root.Path]; exists {
			return nil, fmt.Errorf("mounted destination path is configured more than once")
		}
		byID[root.ID], byPath[root.Path] = root.Path, i
	}
	for _, mount := range discovered {
		if i, exists := byPath[mount.Path]; exists {
			merged[i].GuardMount = true
			merged[i].ProtectedPaths = append([]string(nil), protected...)
			continue
		}
		id := autoMountID(mount.Path)
		if _, exists := byID[id]; exists {
			return nil, fmt.Errorf("automatic mounted destination ID collides with an explicit root")
		}
		if len(merged) >= 32 {
			return nil, fmt.Errorf("too many mounted destinations")
		}
		merged = append(merged, downloads.MountedRoot{ID: id, Label: mount.Label, Path: mount.Path, GuardMount: true, ProtectedPaths: append([]string(nil), protected...)})
		byID[id], byPath[mount.Path] = mount.Path, len(merged)-1
	}
	return merged, nil
}

func prepareDownloadMounts(cfg config.Config) ([]downloads.MountedRoot, error) {
	protected := mountProtectedPaths(cfg)
	discovered, err := downloadfs.DiscoverMounts(protected)
	if err != nil {
		return nil, fmt.Errorf("inspect mounted download folders: %w", err)
	}
	return mergeDownloadMounts(cfg.MountedRoots, discovered, protected)
}
