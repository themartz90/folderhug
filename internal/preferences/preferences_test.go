package preferences

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/store"
)

func preferencesFixture(t *testing.T) (*Service, auth.Principal, profiles.Profile) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vault, err := auth.CreateVault(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	ps := profiles.New(db.DB, vault)
	t.Cleanup(func() { ps.Close(); db.Close() })
	p := auth.Principal{Owner: true, CreatorID: "owner", SessionID: "synthetic-session", ExpiresAt: time.Now().Add(time.Hour)}
	profile, err := ps.Create(context.Background(), profiles.Input{Name: "Synthetic profile", RootURL: "https://files.example.test/root/"}, p)
	if err != nil {
		t.Fatal(err)
	}
	return New(db.DB, ps, catalog.New(db.DB, ps, nil)), p, profile
}

func TestPreferencesDefaultPersistenceAndProfileRemoval(t *testing.T) {
	s, p, profile := preferencesFixture(t)
	ctx := context.Background()
	initial, err := s.Get(ctx, p)
	if err != nil || initial.Theme != "system" || initial.Accent != "amber" || initial.ProfileViews == nil || len(initial.ProfileViews) != 0 {
		t.Fatal(initial, err)
	}
	input := Preferences{Theme: "dark", ProfileViews: map[string]ProfileView{profile.ID: {View: "grid", Sort: "size", Direction: "desc", FoldersFirst: false}}}
	if _, err := s.Put(ctx, p, input); err != nil {
		t.Fatal(err)
	}
	reopened := New(s.db, s.profiles, s.catalog)
	saved, err := reopened.Get(ctx, p)
	if err != nil || saved.Theme != "dark" || saved.Accent != "amber" || saved.ProfileViews[profile.ID] != input.ProfileViews[profile.ID] {
		t.Fatal(saved, err)
	}
	if err := s.profiles.Delete(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	saved, err = s.Get(ctx, p)
	if err != nil || len(saved.ProfileViews) != 0 || saved.Theme != "dark" {
		t.Fatal("deleted profile retained view settings", saved, err)
	}
}

func TestDestinationFoldersPersistAndRejectUnsafeValues(t *testing.T) {
	s, owner, _ := preferencesFixture(t)
	ctx := context.Background()
	last := &DestinationFolder{DestinationID: "media", Path: "Films/2026"}
	input := Preferences{Theme: "system", ProfileViews: map[string]ProfileView{}, LastDestination: last, DestinationFavorites: []DestinationFolder{*last, {DestinationID: "archive", Path: "Photos"}}}
	if _, err := s.Put(ctx, owner, input); err != nil {
		t.Fatal(err)
	}
	saved, err := New(s.db, s.profiles, s.catalog).Get(ctx, owner)
	if err != nil || !reflect.DeepEqual(saved.LastDestination, last) || !reflect.DeepEqual(saved.DestinationFavorites, input.DestinationFavorites) {
		t.Fatalf("destination preferences lost: %+v, %v", saved, err)
	}
	if _, err := s.Put(ctx, owner, Preferences{Theme: "light", ProfileViews: map[string]ProfileView{}}); err != nil {
		t.Fatal(err)
	}
	saved, err = s.Get(ctx, owner)
	if err != nil || !reflect.DeepEqual(saved.LastDestination, last) || !reflect.DeepEqual(saved.DestinationFavorites, input.DestinationFavorites) {
		t.Fatalf("legacy update lost destinations: %+v, %v", saved, err)
	}
	for _, bad := range []DestinationFolder{{DestinationID: "media", Path: "../secret"}, {DestinationID: "media", Path: "/absolute"}, {DestinationID: "media", Path: "folder\\child"}, {DestinationID: "server", Path: "folder"}, {DestinationID: "bad id", Path: ""}} {
		input.LastDestination = &bad
		if _, err := s.Put(ctx, owner, input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted unsafe destination %+v: %v", bad, err)
		}
	}
	input.LastDestination = last
	input.DestinationFavorites = []DestinationFolder{*last, *last}
	if _, err := s.Put(ctx, owner, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted duplicate favorite: %v", err)
	}
}

func TestPreferencesValidationAndOwnerBoundary(t *testing.T) {
	s, p, profile := preferencesFixture(t)
	ctx := context.Background()
	for _, input := range []Preferences{{Theme: "invalid", ProfileViews: map[string]ProfileView{}}, {Theme: "system"}, {Theme: "system", ProfileViews: map[string]ProfileView{profile.ID: {View: "bad", Sort: "name", Direction: "asc"}}}, {Theme: "system", ProfileViews: map[string]ProfileView{profile.ID: {View: "grid", Sort: "bad", Direction: "asc"}}}, {Theme: "system", ProfileViews: map[string]ProfileView{profile.ID: {View: "grid", Sort: "name", Direction: "bad"}}}} {
		if _, err := s.Put(ctx, p, input); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid preferences saved", err)
		}
	}
	if _, err := s.Put(ctx, p, Preferences{Theme: "system", ProfileViews: map[string]ProfileView{"foreign": {View: "grid", Sort: "name", Direction: "asc"}}}); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown profile preference saved", err)
	}
	token := auth.Principal{CreatorID: "api-token", ProfileIDs: []string{profile.ID}, Scopes: []string{"browse:read", "content:read"}}
	if _, err := s.Get(ctx, token); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, token, Preferences{Theme: "dark", ProfileViews: map[string]ProfileView{}}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.ListBookmarks(ctx, token); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.CreateBookmark(ctx, token, BookmarkCreate{ProfileID: profile.ID, DirectoryID: "root", Name: "Root"}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := s.DeleteBookmark(ctx, token, "any"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}

func TestExtendedPreferencesPersistPreserveAndCleanDeletedProfiles(t *testing.T) {
	s, p, profile := preferencesFixture(t)
	ctx := context.Background()
	minimum, maximum, showFolders := int64(100), int64(10000), false
	after, before := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	input := Preferences{Theme: "dark", ProfileViews: map[string]ProfileView{}, Accent: "violet", Density: "compact", ProfileOrder: []string{profile.ID}, FilterPresets: []FilterPreset{
		{ID: "global", Name: "All videos", Filters: PresetFilters{Category: "video"}},
		{ID: "local", Name: "Small images", ProfileID: profile.ID, Filters: PresetFilters{Name: "sample", Category: "image", Extensions: "PNG,jpg", MinSize: &minimum, MaxSize: &maximum, ModifiedAfter: &after, ModifiedBefore: &before, ShowFolders: &showFolders}},
	}}
	if _, err := s.Put(ctx, p, input); err != nil {
		t.Fatal(err)
	}
	restarted := New(s.db, s.profiles, s.catalog)
	saved, err := restarted.Get(ctx, p)
	if err != nil || !reflect.DeepEqual(input, saved) {
		t.Fatalf("restart lost preferences: %+v, %v", saved, err)
	}
	legacy := Preferences{Theme: "light", ProfileViews: map[string]ProfileView{}}
	saved, err = s.Put(ctx, p, legacy)
	input.Theme = "light"
	if err != nil || !reflect.DeepEqual(input, saved) {
		t.Fatalf("legacy update lost preferences: %+v, %v", saved, err)
	}
	if err := s.profiles.Delete(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	saved, err = s.Get(ctx, p)
	if err != nil || len(saved.ProfileOrder) != 0 || len(saved.FilterPresets) != 1 || saved.FilterPresets[0].ID != "global" {
		t.Fatalf("deleted profile references retained: %+v, %v", saved, err)
	}
	if _, err := s.Put(ctx, p, legacy); err != nil {
		t.Fatal(err)
	}
	var encoded string
	if err := s.db.QueryRow(`SELECT settings FROM owner_preferences WHERE id=1`).Scan(&encoded); err != nil || strings.Contains(encoded, profile.ID) {
		t.Fatalf("next save retained deleted profile in storage: %s, %v", encoded, err)
	}
	legacy.FilterPresets, legacy.ProfileOrder = []FilterPreset{}, []string{}
	saved, err = s.Put(ctx, p, legacy)
	if err != nil || saved.FilterPresets == nil || len(saved.FilterPresets) != 0 || saved.ProfileOrder == nil {
		t.Fatalf("explicit clearing failed: %+v, %v", saved, err)
	}
}

func TestLegacyStoredPreferencesGainDefaults(t *testing.T) {
	s, p, _ := preferencesFixture(t)
	if _, err := s.db.Exec(`INSERT INTO owner_preferences(id,settings) VALUES(1,?)`, `{"theme":"dark","profile_views":{}}`); err != nil {
		t.Fatal(err)
	}
	value, err := s.Get(context.Background(), p)
	if err != nil || value.Theme != "dark" || value.Accent != "amber" || value.Density != "comfortable" || value.FilterPresets == nil || value.ProfileOrder == nil {
		t.Fatalf("legacy defaults missing: %+v, %v", value, err)
	}
}

func TestStoredAccentsSurviveDefaultChangeAndPartialUpdates(t *testing.T) {
	for _, accent := range []string{"blue", "teal", "violet", "rose", "amber", "graphite"} {
		t.Run(accent, func(t *testing.T) {
			s, owner, _ := preferencesFixture(t)
			ctx := context.Background()
			stored := `{"theme":"dark","profile_views":{},"accent":"` + accent + `"}`
			if _, err := s.db.ExecContext(ctx, `INSERT INTO owner_preferences(id,settings) VALUES(1,?)`, stored); err != nil {
				t.Fatal(err)
			}
			value, err := s.Get(ctx, owner)
			if err != nil || value.Accent != accent || value.Theme != "dark" {
				t.Fatalf("stored accent changed on read: %+v, %v", value, err)
			}
			value, err = s.Put(ctx, owner, Preferences{Theme: "light", ProfileViews: map[string]ProfileView{}})
			if err != nil || value.Accent != accent {
				t.Fatalf("partial update changed stored accent: %+v, %v", value, err)
			}
			value, err = s.Get(ctx, owner)
			if err != nil || value.Accent != accent || value.Theme != "light" {
				t.Fatalf("saved accent changed after partial update: %+v, %v", value, err)
			}
		})
	}
}

func TestPresetValidationAndBounds(t *testing.T) {
	s, p, profile := preferencesFixture(t)
	negative, small, large := int64(-1), int64(1), int64(100)
	after, before := time.Now(), time.Now().Add(-time.Hour)
	base := func() Preferences {
		return Preferences{Theme: "system", ProfileViews: map[string]ProfileView{}, FilterPresets: []FilterPreset{{ID: "test", Name: "Test", Filters: PresetFilters{}}}}
	}
	for name, mutate := range map[string]func(*Preferences){
		"accent":           func(v *Preferences) { v.Accent = "red" },
		"density":          func(v *Preferences) { v.Density = "dense" },
		"category":         func(v *Preferences) { v.FilterPresets[0].Filters.Category = "executable" },
		"query size":       func(v *Preferences) { v.FilterPresets[0].Filters.Name = strings.Repeat("a", 513) },
		"query control":    func(v *Preferences) { v.FilterPresets[0].Filters.Name = "bad\nname" },
		"extensions size":  func(v *Preferences) { v.FilterPresets[0].Filters.Extensions = strings.Repeat("a", 513) },
		"extensions dot":   func(v *Preferences) { v.FilterPresets[0].Filters.Extensions = ".mp4" },
		"extensions path":  func(v *Preferences) { v.FilterPresets[0].Filters.Extensions = "../mp4" },
		"extensions empty": func(v *Preferences) { v.FilterPresets[0].Filters.Extensions = "mp4,,mkv" },
		"negative size":    func(v *Preferences) { v.FilterPresets[0].Filters.MinSize = &negative },
		"reversed size": func(v *Preferences) {
			v.FilterPresets[0].Filters.MinSize, v.FilterPresets[0].Filters.MaxSize = &large, &small
		},
		"reversed dates": func(v *Preferences) {
			v.FilterPresets[0].Filters.ModifiedAfter, v.FilterPresets[0].Filters.ModifiedBefore = &after, &before
		},
		"blank ID":            func(v *Preferences) { v.FilterPresets[0].ID = "" },
		"ID length":           func(v *Preferences) { v.FilterPresets[0].ID = strings.Repeat("a", 65) },
		"ID chars":            func(v *Preferences) { v.FilterPresets[0].ID = "bad/id" },
		"blank name":          func(v *Preferences) { v.FilterPresets[0].Name = "  " },
		"name length":         func(v *Preferences) { v.FilterPresets[0].Name = strings.Repeat("a", 129) },
		"name control":        func(v *Preferences) { v.FilterPresets[0].Name = "bad\tname" },
		"duplicate presets":   func(v *Preferences) { v.FilterPresets = append(v.FilterPresets, v.FilterPresets[0]) },
		"preset count":        func(v *Preferences) { v.FilterPresets = make([]FilterPreset, 51) },
		"profile order count": func(v *Preferences) { v.ProfileOrder = make([]string, 1001) },
		"duplicate order":     func(v *Preferences) { v.ProfileOrder = []string{profile.ID, profile.ID} },
		"empty order ID":      func(v *Preferences) { v.ProfileOrder = []string{""} },
	} {
		t.Run(name, func(t *testing.T) {
			value := base()
			mutate(&value)
			if _, err := s.Put(context.Background(), p, value); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid setting accepted: %v", err)
			}
		})
	}
	for _, missing := range []Preferences{
		{Theme: "system", ProfileViews: map[string]ProfileView{}, ProfileOrder: []string{"missing"}},
		{Theme: "system", ProfileViews: map[string]ProfileView{}, FilterPresets: []FilterPreset{{ID: "missing", Name: "Missing profile", ProfileID: "missing"}}},
	} {
		if _, err := s.Put(context.Background(), p, missing); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing profile accepted: %v", err)
		}
	}
}

func insertEntry(t *testing.T, s *Service, p profiles.Profile, path, kind string) listing.Entry {
	t.Helper()
	entry := listing.Entry{ID: listing.ID(p.ID, p.Generation, path), ProfileID: p.ID, ParentID: "root", Name: path, EscapedPath: path, Kind: kind, Category: "folder", Provenance: "apache_table"}
	body, _ := json.Marshal(entry)
	_, err := s.db.Exec(`INSERT INTO catalog_entries(profile_id,generation,id,parent_id,escaped_path,metadata) VALUES(?,?,?,?,?,?)`, p.ID, p.Generation, entry.ID, entry.ParentID, entry.EscapedPath, string(body))
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestBookmarksValidateDirectoriesAndInvalidateGenerations(t *testing.T) {
	s, p, profile := preferencesFixture(t)
	ctx := context.Background()
	folder := insertEntry(t, s, profile, "folder/", "directory")
	file := insertEntry(t, s, profile, "file.txt", "file")
	root, err := s.CreateBookmark(ctx, p, BookmarkCreate{ProfileID: profile.ID, DirectoryID: "root", Name: "Root", Position: 2})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.CreateBookmark(ctx, p, BookmarkCreate{ProfileID: profile.ID, DirectoryID: folder.ID, Name: "Folder", Position: 1})
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.ListBookmarks(ctx, p)
	if err != nil || len(items) != 2 || items[0].ID != pinned.ID || items[1].ID != root.ID {
		t.Fatal(items, err)
	}
	if _, err := s.CreateBookmark(ctx, p, BookmarkCreate{ProfileID: profile.ID, DirectoryID: file.ID, Name: "File"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("file bookmarked as folder", err)
	}
	other, err := s.profiles.Create(ctx, profiles.Input{Name: "Other", RootURL: "https://other.example.test/"}, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBookmark(ctx, p, BookmarkCreate{ProfileID: other.ID, DirectoryID: folder.ID, Name: "Foreign folder"}); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatal("cross-profile directory bookmarked", err)
	}
	if _, err := s.profiles.Patch(ctx, profile.ID, map[string]json.RawMessage{"root_url": json.RawMessage(`"https://changed.example.test/root/"`)}, p); err != nil {
		t.Fatal(err)
	}
	items, err = s.ListBookmarks(ctx, p)
	if err != nil || len(items) != 0 {
		t.Fatal("generation change retained stale bookmarks", items, err)
	}
	if _, err := s.CreateBookmark(ctx, p, BookmarkCreate{ProfileID: profile.ID, DirectoryID: folder.ID, Name: "Old folder"}); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatal("stale directory bookmarked", err)
	}
	newRoot, err := s.CreateBookmark(ctx, p, BookmarkCreate{ProfileID: profile.ID, DirectoryID: "root", Name: "New root"})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM bookmarks`).Scan(&count); err != nil || count != 1 {
		t.Fatal("stale rows not cleaned before new bookmark", count, err)
	}
	if err := s.DeleteBookmark(ctx, p, newRoot.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBookmark(ctx, p, newRoot.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestBookmarkInputsAndProfileDeleteCascade(t *testing.T) {
	s, p, profile := preferencesFixture(t)
	ctx := context.Background()
	for _, input := range []BookmarkCreate{{ProfileID: profile.ID, DirectoryID: "root"}, {ProfileID: profile.ID, DirectoryID: "root", Name: "negative", Position: -1}, {ProfileID: profile.ID, DirectoryID: "root", Name: "bad\nname"}, {ProfileID: profile.ID, Name: "no directory"}, {DirectoryID: "root", Name: "no profile"}} {
		if _, err := s.CreateBookmark(ctx, p, input); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid bookmark accepted", err)
		}
	}
	if _, err := s.CreateBookmark(ctx, p, BookmarkCreate{ProfileID: profile.ID, DirectoryID: "root", Name: "Root"}); err != nil {
		t.Fatal(err)
	}
	if err := s.profiles.Delete(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM bookmarks`).Scan(&count); err != nil || count != 0 {
		t.Fatal("profile delete retained bookmarks", count, err)
	}
}
