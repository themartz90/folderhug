// Package releaseinfo supplies the installed build's bundled release summary.
package releaseinfo

import (
	"bytes"
	"embed"
	"encoding/json"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/themartz90/folderhug/internal/buildinfo"
)

//go:embed highlights/*.json
var records embed.FS

type Highlights struct {
	Version    string   `json:"version"`
	AutoShow   bool     `json:"auto_show"`
	Summary    string   `json:"summary"`
	Highlights []string `json:"highlights"`
}

func HighlightsFor(version string) *Highlights {
	if _, ok := buildinfo.ParseVersion(version); version != "dev" && !ok {
		return nil
	}
	data, err := records.ReadFile("highlights/" + version + ".json")
	if err != nil {
		return nil
	}
	return decodeHighlights(data, version)
}

func decodeHighlights(data []byte, version string) *Highlights {
	var encoded struct {
		Version    string   `json:"version"`
		AutoShow   *bool    `json:"auto_show"`
		Summary    string   `json:"summary"`
		Highlights []string `json:"highlights"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&encoded) != nil || encoded.AutoShow == nil {
		return nil
	}
	record := Highlights{Version: encoded.Version, AutoShow: *encoded.AutoShow, Summary: encoded.Summary, Highlights: encoded.Highlights}
	if record.Version != version || !validText(record.Summary, 400) || len(record.Highlights) < 1 || len(record.Highlights) > 8 || (version == "dev" && record.AutoShow) {
		return nil
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil
	}
	for _, bullet := range record.Highlights {
		if !validText(bullet, 240) {
			return nil
		}
	}
	return &record
}

func validText(value string, limit int) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > limit {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func ReleaseURL(version string) string {
	const base = "https://github.com/themartz90/folderhug/releases"
	if _, ok := buildinfo.ParseVersion(version); ok {
		return base + "/tag/" + version
	}
	return base
}
