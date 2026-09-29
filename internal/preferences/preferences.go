package preferences

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/profiles"
)

var (
	ErrForbidden = errors.New("owner session required")
	ErrInvalid   = errors.New("invalid preferences or bookmark")
	ErrNotFound  = errors.New("profile or bookmark not found")
	ErrLimit     = errors.New("bookmark limit reached")
)

type ProfileView struct {
	View         string `json:"view"`
	Sort         string `json:"sort"`
	Direction    string `json:"direction"`
	FoldersFirst bool   `json:"folders_first"`
}

type Preferences struct {
	Theme                string                 `json:"theme"`
	ProfileViews         map[string]ProfileView `json:"profile_views"`
	Accent               string                 `json:"accent"`
	Density              string                 `json:"density"`
	FilterPresets        []FilterPreset         `json:"filter_presets"`
	ProfileOrder         []string               `json:"profile_order"`
	LastDestination      *DestinationFolder     `json:"last_destination,omitempty"`
	DestinationFavorites []DestinationFolder    `json:"destination_favorites,omitempty"`
}

type DestinationFolder struct {
	DestinationID string `json:"destination_id"`
	Path          string `json:"path"`
}

type FilterPreset struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	ProfileID string        `json:"profile_id,omitempty"`
	Filters   PresetFilters `json:"filters"`
}

type PresetFilters struct {
	Name           string     `json:"name,omitempty"`
	Category       string     `json:"category,omitempty"`
	Extensions     string     `json:"extensions,omitempty"`
	MinSize        *int64     `json:"min_size,omitempty"`
	MaxSize        *int64     `json:"max_size,omitempty"`
	ModifiedAfter  *time.Time `json:"modified_after,omitempty"`
	ModifiedBefore *time.Time `json:"modified_before,omitempty"`
	ShowFolders    *bool      `json:"show_folders,omitempty"`
}

type BookmarkCreate struct {
	ProfileID   string `json:"profile_id"`
	DirectoryID string `json:"directory_id"`
	Name        string `json:"name"`
	Position    int    `json:"position"`
}

type Bookmark struct {
	ID          string `json:"id"`
	ProfileID   string `json:"profile_id"`
	DirectoryID string `json:"directory_id"`
	Name        string `json:"name"`
	Position    int    `json:"position"`
}

type Status struct {
	ImageThumbnails    bool   `json:"image_thumbnails"`
	VideoWorker        string `json:"video_worker"`
	CacheMaxBytes      int64  `json:"cache_max_bytes"`
	StreamMaxBytes     int64  `json:"stream_max_bytes"`
	ServerDownloads    bool   `json:"server_downloads"`
	SegmentedDownloads bool   `json:"segmented_downloads"`
}

type StatusProvider func(context.Context) (Status, error)

type Service struct {
	db       *sql.DB
	profiles *profiles.Service
	catalog  *catalog.Service
}

func New(db *sql.DB, profileService *profiles.Service, catalogService *catalog.Service) *Service {
	return &Service{db: db, profiles: profileService, catalog: catalogService}
}

func owner(p auth.Principal) bool {
	return p.Owner && p.CreatorID == "owner" && p.SessionID != "" && p.Can("", "")
}

func (s *Service) Get(ctx context.Context, p auth.Principal) (Preferences, error) {
	if !owner(p) {
		return Preferences{}, ErrForbidden
	}
	return readPreferences(ctx, s.db)
}

type preferenceReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readPreferences(ctx context.Context, reader preferenceReader) (Preferences, error) {
	out := Preferences{Theme: "system", ProfileViews: map[string]ProfileView{}, Accent: "amber", Density: "comfortable", FilterPresets: []FilterPreset{}, ProfileOrder: []string{}}
	var encoded string
	err := reader.QueryRowContext(ctx, `SELECT settings FROM owner_preferences WHERE id=1`).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(encoded), &out); err != nil {
		return Preferences{}, err
	}
	if out.ProfileViews == nil {
		out.ProfileViews = map[string]ProfileView{}
	}
	rows, err := reader.QueryContext(ctx, `SELECT id FROM profiles`)
	if err != nil {
		return Preferences{}, err
	}
	defer rows.Close()
	valid := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return Preferences{}, err
		}
		valid[id] = true
	}
	if err := rows.Err(); err != nil {
		return Preferences{}, err
	}
	for id := range out.ProfileViews {
		if !valid[id] {
			delete(out.ProfileViews, id)
		}
	}
	out.FilterPresets = slices.DeleteFunc(out.FilterPresets, func(preset FilterPreset) bool {
		return preset.ProfileID != "" && !valid[preset.ProfileID]
	})
	out.ProfileOrder = slices.DeleteFunc(out.ProfileOrder, func(id string) bool { return !valid[id] })
	return out, nil
}

func (s *Service) Put(ctx context.Context, p auth.Principal, input Preferences) (Preferences, error) {
	if !owner(p) {
		return Preferences{}, ErrForbidden
	}
	if !slices.Contains([]string{"system", "light", "dark"}, input.Theme) || input.ProfileViews == nil || len(input.ProfileViews) > 1000 {
		return Preferences{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Preferences{}, err
	}
	defer tx.Rollback()
	stored, err := readPreferences(ctx, tx)
	if err != nil {
		return Preferences{}, err
	}
	// Older clients only send theme and profile_views. Merge inside the write transaction.
	if input.Accent == "" {
		input.Accent = stored.Accent
	}
	if input.Density == "" {
		input.Density = stored.Density
	}
	if input.FilterPresets == nil {
		input.FilterPresets = stored.FilterPresets
	}
	if input.ProfileOrder == nil {
		input.ProfileOrder = stored.ProfileOrder
	}
	if input.LastDestination == nil {
		input.LastDestination = stored.LastDestination
	}
	if input.DestinationFavorites == nil {
		input.DestinationFavorites = stored.DestinationFavorites
	}
	if input.LastDestination != nil && !validDestinationFolder(*input.LastDestination) || len(input.DestinationFavorites) > 30 {
		return Preferences{}, ErrInvalid
	}
	seenDestinations := map[DestinationFolder]bool{}
	for _, folder := range input.DestinationFavorites {
		if !validDestinationFolder(folder) || seenDestinations[folder] {
			return Preferences{}, ErrInvalid
		}
		seenDestinations[folder] = true
	}
	if !slices.Contains([]string{"blue", "teal", "violet", "rose", "amber", "graphite"}, input.Accent) || !slices.Contains([]string{"comfortable", "compact"}, input.Density) || len(input.FilterPresets) > 50 || len(input.ProfileOrder) > 1000 {
		return Preferences{}, ErrInvalid
	}
	referenced := map[string]bool{}
	presetIDs := map[string]bool{}
	for _, preset := range input.FilterPresets {
		if !validPreset(preset) || presetIDs[preset.ID] {
			return Preferences{}, ErrInvalid
		}
		presetIDs[preset.ID] = true
		if preset.ProfileID != "" {
			referenced[preset.ProfileID] = true
		}
	}
	orderIDs := map[string]bool{}
	for _, id := range input.ProfileOrder {
		if id == "" || len(id) > 128 || orderIDs[id] {
			return Preferences{}, ErrInvalid
		}
		orderIDs[id], referenced[id] = true, true
	}
	for id, view := range input.ProfileViews {
		if !slices.Contains([]string{"detailed", "compact", "grid"}, view.View) || !slices.Contains([]string{"name", "size", "modified", "type"}, view.Sort) || !slices.Contains([]string{"asc", "desc"}, view.Direction) {
			return Preferences{}, ErrInvalid
		}
		referenced[id] = true
	}
	for id := range referenced {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM profiles WHERE id=?`, id).Scan(&exists); err != nil {
			return Preferences{}, err
		}
		if exists != 1 {
			return Preferences{}, ErrNotFound
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return Preferences{}, err
	}
	if len(encoded) > 64<<10 {
		return Preferences{}, ErrInvalid
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO owner_preferences(id,settings) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET settings=excluded.settings`, string(encoded)); err != nil {
		return Preferences{}, err
	}
	if err := tx.Commit(); err != nil {
		return Preferences{}, err
	}
	return input, nil
}

func validDestinationFolder(folder DestinationFolder) bool {
	if folder.DestinationID == "" || len(folder.DestinationID) > 64 || strings.IndexFunc(folder.DestinationID, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) >= 0 || len(folder.Path) > 1024 || !utf8.ValidString(folder.Path) {
		return false
	}
	if folder.DestinationID == "server" && folder.Path != "" {
		return false
	}
	if folder.Path == "" {
		return true
	}
	for _, part := range strings.Split(folder.Path, "/") {
		if part == "" || part == "." || part == ".." || strings.Contains(part, "\\") || strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}

func validPreset(preset FilterPreset) bool {
	if preset.ID == "" || len(preset.ID) > 64 || strings.IndexFunc(preset.ID, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) >= 0 || strings.TrimSpace(preset.Name) == "" || utf8.RuneCountInString(preset.Name) > 128 || !utf8.ValidString(preset.Name) || strings.IndexFunc(preset.Name, unicode.IsControl) >= 0 || len(preset.ProfileID) > 128 {
		return false
	}
	f := preset.Filters
	if len(f.Name) > 512 || !utf8.ValidString(f.Name) || strings.IndexFunc(f.Name, unicode.IsControl) >= 0 || len(f.Extensions) > 512 || !slices.Contains([]string{"", "folder", "video", "image", "audio", "document", "archive", "other"}, f.Category) {
		return false
	}
	if f.MinSize != nil && *f.MinSize < 0 || f.MaxSize != nil && *f.MaxSize < 0 || f.MinSize != nil && f.MaxSize != nil && *f.MinSize > *f.MaxSize {
		return false
	}
	if f.ModifiedAfter != nil && f.ModifiedBefore != nil && f.ModifiedAfter.After(*f.ModifiedBefore) {
		return false
	}
	if f.Extensions != "" {
		for _, ext := range strings.Split(f.Extensions, ",") {
			ext = strings.TrimSpace(ext)
			if ext == "" || strings.ContainsAny(ext, "./\\ \t\r\n") || strings.IndexFunc(ext, unicode.IsControl) >= 0 || !utf8.ValidString(ext) {
				return false
			}
		}
	}
	return true
}

func (s *Service) ListBookmarks(ctx context.Context, p auth.Principal) ([]Bookmark, error) {
	if !owner(p) {
		return nil, ErrForbidden
	}
	rows, err := s.db.QueryContext(ctx, `SELECT b.id,b.profile_id,b.directory_id,b.name,b.position FROM bookmarks b JOIN profiles p ON p.id=b.profile_id AND p.generation=b.generation ORDER BY b.position,b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Bookmark{}
	for rows.Next() {
		var item Bookmark
		if err := rows.Scan(&item.ID, &item.ProfileID, &item.DirectoryID, &item.Name, &item.Position); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) CreateBookmark(ctx context.Context, p auth.Principal, input BookmarkCreate) (Bookmark, error) {
	if !owner(p) {
		return Bookmark{}, ErrForbidden
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.ProfileID == "" || len(input.ProfileID) > 128 || input.DirectoryID == "" || len(input.DirectoryID) > 128 || input.Name == "" || utf8.RuneCountInString(input.Name) > 128 || !utf8.ValidString(input.Name) || input.Position < 0 || strings.IndexFunc(input.Name, unicode.IsControl) >= 0 {
		return Bookmark{}, ErrInvalid
	}
	profile, err := s.profiles.Get(ctx, input.ProfileID, profiles.LeaseID(p))
	if err != nil {
		return Bookmark{}, err
	}
	if input.DirectoryID != "root" {
		entry, err := s.catalog.Entry(ctx, input.ProfileID, profiles.LeaseID(p), input.DirectoryID)
		if err != nil {
			return Bookmark{}, err
		}
		if entry.Kind != "directory" {
			return Bookmark{}, ErrInvalid
		}
	}
	item := Bookmark{ID: rand.Text(), ProfileID: input.ProfileID, DirectoryID: input.DirectoryID, Name: input.Name, Position: input.Position}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Bookmark{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM bookmarks WHERE NOT EXISTS(SELECT 1 FROM profiles p WHERE p.id=bookmarks.profile_id AND p.generation=bookmarks.generation)`); err != nil {
		return Bookmark{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM bookmarks`).Scan(&count); err != nil {
		return Bookmark{}, err
	}
	if count >= 1000 {
		return Bookmark{}, ErrLimit
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO bookmarks(id,profile_id,generation,directory_id,name,position) SELECT ?,id,generation,?,?,? FROM profiles WHERE id=? AND generation=?`, item.ID, item.DirectoryID, item.Name, item.Position, profile.ID, profile.Generation)
	if err != nil {
		return Bookmark{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Bookmark{}, err
	}
	if n != 1 {
		return Bookmark{}, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return Bookmark{}, err
	}
	return item, nil
}

func (s *Service) DeleteBookmark(ctx context.Context, p auth.Principal, id string) error {
	if !owner(p) {
		return ErrForbidden
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM bookmarks WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
