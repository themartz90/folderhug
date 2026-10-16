// Package buildinfo describes the binary's build without opening application state.
package buildinfo

import (
	"regexp"
	"strings"
)

// Version and Revision are set by release builds using Go linker flags.
var Version = "dev"
var Revision = "unknown"

type Info struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Channel  string `json:"channel"`
}

var versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-rc\.([1-9][0-9]*))?$`)
var revisionPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

func Current() Info {
	info := Info{Version: "dev", Revision: "unknown", Channel: "development"}
	if revisionPattern.MatchString(Revision) {
		info.Revision = Revision
	}
	version, ok := ParseVersion(Version)
	if !ok || info.Revision == "unknown" {
		return info
	}
	info.Version, info.Channel = Version, "stable"
	if version.IsPrerelease() {
		info.Channel = "prerelease"
	}
	return info
}

// ParsedVersion retains decimal components as strings so comparison cannot overflow.
type ParsedVersion struct{ major, minor, patch, rc string }

func ParseVersion(raw string) (ParsedVersion, bool) {
	parts := versionPattern.FindStringSubmatch(raw)
	if parts == nil {
		return ParsedVersion{}, false
	}
	return ParsedVersion{parts[1], parts[2], parts[3], parts[4]}, true
}

func (v ParsedVersion) IsPrerelease() bool { return v.rc != "" }

func (v ParsedVersion) Compare(other ParsedVersion) int {
	for _, pair := range [][2]string{{v.major, other.major}, {v.minor, other.minor}, {v.patch, other.patch}} {
		if result := compareDecimal(pair[0], pair[1]); result != 0 {
			return result
		}
	}
	if v.rc == other.rc {
		return 0
	}
	if v.rc == "" {
		return 1
	}
	if other.rc == "" {
		return -1
	}
	return compareDecimal(v.rc, other.rc)
}

func compareDecimal(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
