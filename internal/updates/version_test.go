package updates

import "testing"

func TestVersionComparison(t *testing.T) {
	for _, tc := range []struct{ current, latest, want string }{
		{"v0.9.0", "v0.10.0", "available"}, {"v1.0.0", "v1.0.0", "current"}, {"v2.0.0", "v1.9.9", "ahead"},
		{"v1.0.0-rc.2", "v1.0.0-rc.10", "available"}, {"v1.0.0-rc.10", "v1.0.0-rc.2", "ahead"},
		{"v1.0.0-rc.2", "v1.0.0", "available"}, {"v1.0.0", "v1.0.0-rc.2", "ahead"},
		{"dev", "v1.0.0", "unknown"}, {"v1.0.0", "invalid", "unknown"},
	} {
		if got := compare(tc.current, tc.latest); got != tc.want {
			t.Errorf("%s / %s: %s want %s", tc.current, tc.latest, got, tc.want)
		}
	}
}
