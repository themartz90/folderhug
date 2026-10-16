package releaseinfo

import (
	"strings"
	"testing"
)

func TestHighlightsFor(t *testing.T) {
	h := HighlightsFor("dev")
	if h == nil || h.Version != "dev" || h.AutoShow || len(h.Highlights) == 0 {
		t.Fatalf("invalid development preview: %+v", h)
	}
	h.Highlights[0] = "changed"
	if HighlightsFor("dev").Highlights[0] == "changed" {
		t.Fatal("caller mutated embedded record")
	}
	for _, version := range []string{"v99.99.99", "v99.99.99-rc.2", "../dev", "DEV", ""} {
		if HighlightsFor(version) != nil {
			t.Errorf("unexpected notes for %q", version)
		}
	}
}

func TestDecodeHighlights(t *testing.T) {
	valid := `{"version":"v1.2.3-rc.1","auto_show":true,"summary":"A short summary.","highlights":["A change."]}`
	h := decodeHighlights([]byte(valid), "v1.2.3-rc.1")
	if h == nil || !h.AutoShow {
		t.Fatal("valid record rejected")
	}
	invalid := []string{
		strings.Replace(valid, `"auto_show":true,`, "", 1),
		strings.Replace(valid, `"auto_show":true`, `"auto_show":null`, 1),
		strings.Replace(valid, "v1.2.3-rc.1", "v1.2.3", 1),
		strings.Replace(valid, "A short summary.", strings.Repeat("a", 401), 1),
		strings.Replace(valid, "A change.", strings.Repeat("a", 241), 1),
		strings.Replace(valid, `["A change."]`, `[]`, 1),
		strings.Replace(valid, "A short summary.", "   ", 1),
		strings.Replace(valid, "A change.", `line\nbreak`, 1),
		strings.Replace(valid, `["A change."]`, `["a","b","c","d","e","f","g","h","i"]`, 1),
		valid + `{}`, `null`,
	}
	for _, record := range invalid {
		if decodeHighlights([]byte(record), "v1.2.3-rc.1") != nil {
			t.Errorf("accepted invalid record: %.100s", record)
		}
	}
	unicode := strings.Replace(valid, "A short summary.", strings.Repeat("é", 400), 1)
	if decodeHighlights([]byte(unicode), "v1.2.3-rc.1") == nil {
		t.Fatal("character limit counted bytes")
	}
}

func TestReleaseURL(t *testing.T) {
	for _, version := range []string{"v1.2.3", "v1.2.3-rc.1"} {
		if got := ReleaseURL(version); got != "https://github.com/themartz90/folderhug/releases/tag/"+version {
			t.Errorf("URL = %q", got)
		}
	}
	for _, version := range []string{"dev", "../x", "v1.2.3?x", ""} {
		if got := ReleaseURL(version); got != "https://github.com/themartz90/folderhug/releases" {
			t.Errorf("unsafe URL: %q", got)
		}
	}
}
