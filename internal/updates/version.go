package updates

import "github.com/themartz90/folderhug/internal/buildinfo"

func compare(current, latest string) string {
	installed, ok := buildinfo.ParseVersion(current)
	if !ok {
		return "unknown"
	}
	released, ok := buildinfo.ParseVersion(latest)
	if !ok {
		return "unknown"
	}
	switch installed.Compare(released) {
	case -1:
		return "available"
	case 0:
		return "current"
	default:
		return "ahead"
	}
}
