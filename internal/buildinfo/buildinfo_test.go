package buildinfo

import "testing"

func TestCurrent(t *testing.T) {
	oldVersion, oldRevision := Version, Revision
	defer func() { Version, Revision = oldVersion, oldRevision }()
	sha := "0123456789abcdef0123456789abcdef01234567"
	for _, tt := range []struct{ version, revision, wantVersion, wantRevision, channel string }{
		{"dev", "unknown", "dev", "unknown", "development"},
		{"v1.2.3", sha, "v1.2.3", sha, "stable"},
		{"v1.2.3-rc.10", sha, "v1.2.3-rc.10", sha, "prerelease"},
		{"v01.2.3", sha, "dev", sha, "development"},
		{"v1.2.3", "bad\nrevision", "dev", "unknown", "development"},
		{"v1.2.3", "unknown", "dev", "unknown", "development"},
	} {
		Version, Revision = tt.version, tt.revision
		got := Current()
		if got.Version != tt.wantVersion || got.Revision != tt.wantRevision || got.Channel != tt.channel {
			t.Errorf("Current(%q,%q) = %+v", tt.version, tt.revision, got)
		}
	}
}

func TestParseAndCompare(t *testing.T) {
	for _, invalid := range []string{"dev", "1.2.3", "v01.2.3", "v1.2", "v1.2.3-rc.0", "v1.2.3-rc.01", "v1.2.3-beta.1", "v1.2.3+build", " v1.2.3", "v1.2.3\n"} {
		if _, ok := ParseVersion(invalid); ok {
			t.Errorf("accepted %q", invalid)
		}
	}
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"v0.9.0", "v0.10.0", -1}, {"v1.2.3", "v1.2.3", 0},
		{"v1.2.3-rc.10", "v1.2.3-rc.2", 1}, {"v1.2.3", "v1.2.3-rc.10", 1},
		{"v1.2.3-rc.1", "v1.2.4-rc.1", -1}, {"v2.0.0", "v1.99.99", 1},
		{"v99999999999999999999999999.0.0", "v2.0.0", 1},
	} {
		a, okA := ParseVersion(tt.a)
		b, okB := ParseVersion(tt.b)
		if !okA || !okB {
			t.Fatalf("failed to parse %q or %q", tt.a, tt.b)
		}
		if got := a.Compare(b); got != tt.want {
			t.Errorf("%s.Compare(%s) = %d", tt.a, tt.b, got)
		}
		if got := b.Compare(a); got != -tt.want {
			t.Errorf("reverse compare = %d", got)
		}
	}
	stable, _ := ParseVersion("v1.2.3")
	rc, _ := ParseVersion("v1.2.3-rc.2")
	if stable.IsPrerelease() || !rc.IsPrerelease() {
		t.Fatal("wrong prerelease channel")
	}
}
