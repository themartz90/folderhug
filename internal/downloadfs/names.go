package downloadfs

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// SafeName produces one portable filename. Each job has its own collision domain.
func SafeName(raw string) string {
	name := norm.NFC.String(strings.ToValidUTF8(raw, "_"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == 0x061c || r == 0x200e || r == 0x200f || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 || r == 0xfeff {
			return -1
		}
		if strings.ContainsRune(`/\<>:"|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" {
		name = "download"
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	reserved := stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || stem == "CONIN$" || stem == "CONOUT$" || stem == "CLOCK$"
	if strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT") {
		suffix := strings.TrimPrefix(strings.TrimPrefix(stem, "COM"), "LPT")
		reserved = reserved || len([]rune(suffix)) == 1 && strings.ContainsAny(suffix, "123456789¹²³")
	}
	if reserved {
		name = "_" + name
	}
	for len(name) > 180 {
		_, n := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-n]
	}
	name = strings.TrimRight(name, " .")
	if name == "" {
		return "download"
	}
	return name
}

func validID(id string) bool {
	if len(id) < 16 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validName(name string) bool { return name != ".part" && name == SafeName(name) }
