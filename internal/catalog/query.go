package catalog

import (
	"cmp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/themartz90/folderhug/internal/listing"
)

type Query struct {
	Limit          int
	Cursor         string
	Name           string
	Category       string
	Extensions     string
	MinSize        *int64
	MaxSize        *int64
	ModifiedAfter  *time.Time
	ModifiedBefore *time.Time
	ShowFolders    *bool
	Sort           string
	Direction      string
	FoldersFirst   *bool
	Refresh        bool
}

func normalize(q Query) (Query, error) {
	if q.Limit == 0 {
		q.Limit = 200
	}
	if q.Sort == "" {
		q.Sort = "name"
	}
	if q.Direction == "" {
		q.Direction = "asc"
	}
	if q.ShowFolders == nil {
		q.ShowFolders = boolPointer(true)
	}
	if q.FoldersFirst == nil {
		q.FoldersFirst = boolPointer(true)
	}
	if q.Limit < 1 || q.Limit > 1000 || len(q.Cursor) > 2048 || len(q.Name) > 512 || len(q.Extensions) > 512 || !utf8.ValidString(q.Name) || q.Refresh && q.Cursor != "" {
		return q, ErrInvalid
	}
	if !slices.Contains([]string{"name", "size", "modified", "type"}, q.Sort) || !slices.Contains([]string{"asc", "desc"}, q.Direction) {
		return q, ErrInvalid
	}
	if !slices.Contains([]string{"", "folder", "video", "image", "audio", "document", "archive", "other"}, q.Category) {
		return q, ErrInvalid
	}
	if q.MinSize != nil && *q.MinSize < 0 || q.MaxSize != nil && *q.MaxSize < 0 || q.MinSize != nil && q.MaxSize != nil && *q.MinSize > *q.MaxSize {
		return q, ErrInvalid
	}
	if q.ModifiedAfter != nil && q.ModifiedBefore != nil && q.ModifiedAfter.After(*q.ModifiedBefore) {
		return q, ErrInvalid
	}
	q.Name = strings.ToLower(q.Name)
	if q.Extensions != "" {
		extensions := strings.Split(strings.ToLower(q.Extensions), ",")
		for i, ext := range extensions {
			ext = strings.TrimSpace(ext)
			if ext == "" || strings.ContainsAny(ext, "./\\ \t\r\n") || !utf8.ValidString(ext) {
				return q, ErrInvalid
			}
			extensions[i] = ext
		}
		slices.Sort(extensions)
		q.Extensions = strings.Join(slices.Compact(extensions), ",")
	}
	return q, nil
}

func boolPointer(v bool) *bool { return &v }

func selected(entries []listing.Entry, q Query) []listing.Entry {
	out := make([]listing.Entry, 0, len(entries))
	var extensions []string
	if q.Extensions != "" {
		extensions = strings.Split(q.Extensions, ",")
	}
	for _, entry := range entries {
		if !*q.ShowFolders && entry.Kind == "directory" || q.Name != "" && !strings.Contains(strings.ToLower(entry.Name), q.Name) || q.Category != "" && entry.Category != q.Category {
			continue
		}
		if len(extensions) > 0 && (entry.Extension == nil || !slices.Contains(extensions, strings.ToLower(*entry.Extension))) {
			continue
		}
		if q.MinSize != nil && (entry.Size == nil || *entry.Size < *q.MinSize) || q.MaxSize != nil && (entry.Size == nil || *entry.Size > *q.MaxSize) {
			continue
		}
		if q.ModifiedAfter != nil && (entry.ModifiedAt == nil || !entry.TimezoneKnown || entry.ModifiedAt.Before(*q.ModifiedAfter)) || q.ModifiedBefore != nil && (entry.ModifiedAt == nil || !entry.TimezoneKnown || entry.ModifiedAt.After(*q.ModifiedBefore)) {
			continue
		}
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b listing.Entry) int {
		if *q.FoldersFirst && (a.Kind == "directory") != (b.Kind == "directory") {
			if a.Kind == "directory" {
				return -1
			}
			return 1
		}
		var order int
		switch q.Sort {
		case "size":
			if (a.Size == nil) != (b.Size == nil) {
				if a.Size == nil {
					return 1
				}
				return -1
			}
			if a.Size != nil {
				order = cmp.Compare(*a.Size, *b.Size)
			}
		case "modified":
			if (a.ModifiedAt == nil) != (b.ModifiedAt == nil) {
				if a.ModifiedAt == nil {
					return 1
				}
				return -1
			}
			if a.ModifiedAt != nil {
				order = a.ModifiedAt.Compare(*b.ModifiedAt)
			}
		case "type":
			order = strings.Compare(a.Category, b.Category)
		default:
			order = natural(a.Name, b.Name)
		}
		if order == 0 {
			order = strings.Compare(a.ID, b.ID)
		}
		if q.Direction == "desc" {
			return -order
		}
		return order
	})
	return out
}

// Compare digit runs without parsing integers, including runs larger than int64.
func natural(a, b string) int {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for len(a) > 0 && len(b) > 0 {
		if a[0] >= '0' && a[0] <= '9' && b[0] >= '0' && b[0] <= '9' {
			i, j := 0, 0
			for i < len(a) && a[i] >= '0' && a[i] <= '9' {
				i++
			}
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			x, y := strings.TrimLeft(a[:i], "0"), strings.TrimLeft(b[:j], "0")
			if c := cmp.Compare(len(x), len(y)); c != 0 {
				return c
			}
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
			a, b = a[i:], b[j:]
			continue
		}
		x, ix := utf8.DecodeRuneInString(a)
		y, iy := utf8.DecodeRuneInString(b)
		if c := cmp.Compare(x, y); c != 0 {
			return c
		}
		a, b = a[ix:], b[iy:]
	}
	return cmp.Compare(len(a), len(b))
}
