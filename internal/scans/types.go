package scans

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/upstream"
)

var (
	ErrInvalid             = errors.New("invalid scan or search request")
	ErrNotFound            = errors.New("scan not found")
	ErrInvalidState        = errors.New("scan action is not available")
	ErrIdempotencyConflict = errors.New("idempotency key has a different request")
	ErrSnapshotExpired     = errors.New("search snapshot expired")
	ErrBudget              = errors.New("scan or search budget exceeded")
)

type Input struct {
	DirectoryID         string   `json:"directory_id"`
	MaxDepth            *int     `json:"max_depth,omitempty"`
	MaxEntries          *int     `json:"max_entries,omitempty"`
	MaxSeconds          *int     `json:"max_seconds,omitempty"`
	ExcludedDirectories []string `json:"excluded_directories,omitempty"`
	Categories          []string `json:"categories,omitempty"`
}
type ApiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}
type Coverage struct {
	ProfileID   string     `json:"profile_id"`
	DirectoryID string     `json:"directory_id"`
	State       string     `json:"state"`
	LastScanAt  *time.Time `json:"last_scan_at"`
}
type Scan struct {
	ID                 string    `json:"id"`
	ProfileID          string    `json:"profile_id"`
	DirectoryID        string    `json:"directory_id"`
	State              string    `json:"state"`
	DirectoriesVisited int       `json:"directories_visited"`
	FilesFound         int       `json:"files_found"`
	DirectoriesFailed  int       `json:"directories_failed"`
	DirectoriesSkipped int       `json:"directories_skipped"`
	ElapsedSeconds     float64   `json:"elapsed_seconds"`
	Coverage           Coverage  `json:"coverage"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	Error              *ApiError `json:"error"`
}
type ScanPage struct {
	Scans      []Scan  `json:"scans"`
	NextCursor *string `json:"next_cursor"`
}
type SearchQuery struct {
	catalog.Query
	Q, Match, ProfileID, DirectoryID string
	DirectOnly                       bool
	Categories                       []string
}
type SearchPage struct {
	Entries    []listing.Entry `json:"entries"`
	NextCursor *string         `json:"next_cursor"`
	Coverage   []Coverage      `json:"coverage"`
}
type job struct {
	Scan
	Generation                           int
	CreatorID, LeaseID, Origin, RootPath string
	Owner                                bool
	Input                                Input
	EntriesSeen                          int
	Partial                              bool
}

func integer(v int) *int { return &v }
func normalizeInput(in Input) (Input, error) {
	if in.DirectoryID == "" {
		in.DirectoryID = "root"
	}
	if in.MaxDepth == nil {
		in.MaxDepth = integer(64)
	}
	if in.MaxEntries == nil {
		in.MaxEntries = integer(100000)
	}
	if in.MaxSeconds == nil {
		in.MaxSeconds = integer(1800)
	}
	if len(in.DirectoryID) > 128 || *in.MaxDepth < 0 || *in.MaxDepth > 64 || *in.MaxEntries < 1 || *in.MaxEntries > 100000 || *in.MaxSeconds < 1 || *in.MaxSeconds > 1800 || len(in.ExcludedDirectories) > 100 {
		return in, ErrInvalid
	}
	for _, category := range in.Categories {
		if !slices.Contains([]string{"folder", "video", "image", "audio", "document", "archive", "other"}, category) {
			return in, ErrInvalid
		}
	}
	in.Categories = slices.Clone(in.Categories)
	slices.Sort(in.Categories)
	in.Categories = slices.Compact(in.Categories)
	in.ExcludedDirectories = slices.Clone(in.ExcludedDirectories)
	root, _ := upstream.NewRoot("https://example.invalid/", false)
	for i, path := range in.ExcludedDirectories {
		if path == "" || len(path) > 512 {
			return in, ErrInvalid
		}
		if _, err := root.Resolve(path); err != nil {
			return in, ErrInvalid
		}
		in.ExcludedDirectories[i] = strings.TrimSuffix(path, "/") + "/"
	}
	slices.Sort(in.ExcludedDirectories)
	in.ExcludedDirectories = slices.Compact(in.ExcludedDirectories)
	return in, nil
}

func naturalKey(name string) string {
	name = strings.ToLower(name)
	var key strings.Builder
	for len(name) > 0 {
		if name[0] >= '0' && name[0] <= '9' {
			i := 0
			for i < len(name) && name[i] >= '0' && name[i] <= '9' {
				i++
			}
			digits := strings.TrimLeft(name[:i], "0")
			length := len(digits)
			fmt.Fprintf(&key, "0%04d:%s:", length, digits)
			name = name[i:]
		} else {
			key.WriteByte(name[0])
			name = name[1:]
		}
	}
	return key.String()
}
