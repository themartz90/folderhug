package catalog

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/upstream"
)

const (
	FreshTTL      = 60 * time.Second
	SnapshotTTL   = 5 * time.Minute
	maxSnapshots  = 64
	maxEntries    = 100000
	maxCacheBytes = 64 << 20
)

var (
	ErrInvalid           = errors.New("invalid browse query")
	ErrNotFound          = errors.New("entry not found")
	ErrSnapshotExpired   = errors.New("listing snapshot expired")
	ErrSourceAuth        = errors.New("source credentials were rejected")
	ErrSourceUnavailable = errors.New("source listing is unavailable")
	ErrBudget            = errors.New("listing cache budget exceeded")
)

type Fetcher interface {
	Do(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error)
}

type Page struct {
	Entries      []listing.Entry `json:"entries"`
	SnapshotID   string          `json:"snapshot_id"`
	NextCursor   *string         `json:"next_cursor"`
	Stale        bool            `json:"stale"`
	FetchedAt    time.Time       `json:"fetched_at"`
	DirectoryID  string          `json:"directory_id"`
	ParentID     *string         `json:"parent_id"`
	TotalEntries int             `json:"total_entries"`
}

type snapshot struct {
	id          string
	key         string
	entries     []listing.Entry
	fetched     time.Time
	directoryID string
	parentID    *string
	bytes       int
}

type Service struct {
	db         *sql.DB
	profiles   *profiles.Service
	fetcher    Fetcher
	mu         sync.Mutex
	snapshots  map[string]*snapshot
	latest     map[string]string
	key        []byte
	fetchSlots chan struct{}
	pageSlots  chan struct{}
	now        func() time.Time
}

func New(db *sql.DB, profileService *profiles.Service, fetcher Fetcher) *Service {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &Service{db: db, profiles: profileService, fetcher: fetcher, snapshots: make(map[string]*snapshot), latest: make(map[string]string), key: key, fetchSlots: make(chan struct{}, 2), pageSlots: make(chan struct{}, 2), now: time.Now}
}

// Entry resolves only identities previously obtained from a checked listing.
// Profile scope authorization is the caller's responsibility before this call.
func (s *Service) Entry(ctx context.Context, profileID, leaseID, entryID string) (listing.Entry, error) {
	p, _, active, cancel, err := s.profiles.Access(ctx, profileID, leaseID)
	if err != nil {
		return listing.Entry{}, err
	}
	defer cancel()
	return s.entry(active, p, entryID)
}

func (s *Service) entry(ctx context.Context, p profiles.Profile, id string) (listing.Entry, error) {
	if id == "root" {
		return listing.Entry{ID: "root", ProfileID: p.ID, Name: p.Name, Kind: "directory", Category: "folder", Provenance: "response_headers"}, nil
	}
	if len(id) != 43 {
		return listing.Entry{}, ErrNotFound
	}
	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT metadata FROM catalog_entries WHERE profile_id=? AND generation=? AND id=?`, p.ID, p.Generation, id).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return listing.Entry{}, ErrNotFound
	}
	if err != nil {
		return listing.Entry{}, err
	}
	var entry listing.Entry
	if err = json.Unmarshal([]byte(encoded), &entry); err != nil {
		return entry, err
	}
	root, err := upstream.NewRoot(p.RootURL, p.AllowHTTP)
	if err != nil {
		return listing.Entry{}, err
	}
	if _, err = root.Resolve(entry.EscapedPath); err != nil {
		return listing.Entry{}, ErrNotFound
	}
	if entry.ProfileID != p.ID || entry.ID != id || listing.ID(p.ID, p.Generation, entry.EscapedPath) != id {
		return listing.Entry{}, ErrNotFound
	}
	return entry, nil
}

func (s *Service) Browse(ctx context.Context, profileID, leaseID, directoryID string, q Query) (Page, error) {
	q, err := normalize(q)
	if err != nil {
		return Page{}, err
	}
	if directoryID == "" {
		directoryID = "root"
	}
	p, credentials, active, cancel, err := s.profiles.Access(ctx, profileID, leaseID)
	if err != nil {
		return Page{}, err
	}
	defer cancel()
	directory, err := s.entry(active, p, directoryID)
	if err != nil {
		return Page{}, err
	}
	if directory.Kind != "directory" {
		return Page{}, ErrInvalid
	}
	key := snapshotKey(p, leaseID, directoryID, credentials)
	if q.Cursor != "" {
		return s.cursorPage(active, key, q)
	}
	previous := s.latestSnapshot(key)
	if previous != nil && !q.Refresh && s.now().Sub(previous.fetched) < FreshTTL {
		return s.page(active, previous, q, 0, false)
	}
	select {
	case s.fetchSlots <- struct{}{}:
		defer func() { <-s.fetchSlots }()
	case <-active.Done():
		return Page{}, active.Err()
	}
	if !q.Refresh {
		if recent := s.latestSnapshot(key); recent != nil && s.now().Sub(recent.fetched) < FreshTTL {
			return s.page(active, recent, q, 0, false)
		}
	}
	snap, err := s.fetch(active, p, credentials, directory, key)
	if err != nil {
		if active.Err() != nil {
			return Page{}, active.Err()
		}
		if previous != nil && s.now().Sub(previous.fetched) < SnapshotTTL && staleAllowed(err) {
			return s.page(active, previous, q, 0, true)
		}
		return Page{}, err
	}
	if err = s.cache(snap); err != nil {
		return Page{}, err
	}
	return s.page(active, snap, q, 0, false)
}

func snapshotKey(p profiles.Profile, leaseID, directoryID string, credentials *upstream.Credentials) string {
	encoded, _ := json.Marshal(credentials)
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%x", p.ID, p.Generation, leaseID, directoryID, digest)
}

func staleAllowed(err error) bool {
	return errors.Is(err, ErrSourceUnavailable) || errors.Is(err, upstream.ErrNetwork) || errors.Is(err, upstream.ErrTimeout) || errors.Is(err, context.DeadlineExceeded)
}

func (s *Service) fetch(ctx context.Context, p profiles.Profile, credentials *upstream.Credentials, directory listing.Entry, key string) (*snapshot, error) {
	root, err := upstream.NewRoot(p.RootURL, p.AllowHTTP)
	if err != nil {
		return nil, err
	}
	response, err := s.fetcher.Do(ctx, root, http.MethodGet, directory.EscapedPath, upstream.Options{Credentials: credentials, MaxBytes: listing.MaxBytes, Timeout: 30 * time.Second, Interactive: true})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, ErrSourceAuth
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return nil, ErrBudget
	}
	if response.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if response.StatusCode != http.StatusOK {
		return nil, ErrSourceUnavailable
	}
	current, err := root.Resolve(directory.EscapedPath)
	if err != nil {
		return nil, err
	}
	if response.Request != nil && response.Request.URL != nil {
		// Directory aliases would introduce parent identities that were never
		// discovered in a listing. Only trailing-slash canonicalization is supported.
		if strings.TrimSuffix(response.Request.URL.Path, "/") != strings.TrimSuffix(current.Path, "/") {
			return nil, listing.ErrUnsupported
		}
		current = response.Request.URL
	}
	entries, _, err := listing.Parse(response.Body, root, current, p.ID, p.Generation)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = s.persist(ctx, p, entries); err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(entries)
	snap := &snapshot{id: rand.Text(), key: key, entries: entries, fetched: s.now().UTC(), directoryID: directory.ID, bytes: len(encoded) + len(entries)*128}
	if directory.ID != "root" {
		parent := directory.ParentID
		snap.parentID = &parent
	}
	return snap, nil
}

func (s *Service) persist(ctx context.Context, p profiles.Profile, entries []listing.Entry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var generation int
	if err = tx.QueryRowContext(ctx, "SELECT generation FROM profiles WHERE id=?", p.ID).Scan(&generation); err != nil {
		return err
	}
	if generation != p.Generation {
		return ErrNotFound
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO catalog_entries(profile_id,generation,id,parent_id,escaped_path,metadata) VALUES(?,?,?,?,?,?) ON CONFLICT(profile_id,generation,id) DO UPDATE SET parent_id=excluded.parent_id,escaped_path=excluded.escaped_path,metadata=excluded.metadata`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if _, err = statement.ExecContext(ctx, p.ID, p.Generation, entry.ID, entry.ParentID, entry.EscapedPath, string(encoded)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) latestSnapshot(key string) *snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	return s.snapshots[s.latest[key]]
}

func (s *Service) prune() {
	now := s.now()
	for id, snap := range s.snapshots {
		if now.Sub(snap.fetched) >= SnapshotTTL {
			s.remove(id)
		}
	}
}

func (s *Service) remove(id string) {
	if snap := s.snapshots[id]; snap != nil {
		if s.latest[snap.key] == id {
			delete(s.latest, snap.key)
		}
		delete(s.snapshots, id)
	}
}

func (s *Service) cache(snap *snapshot) error {
	if len(snap.entries) > maxEntries || snap.bytes > maxCacheBytes {
		return ErrBudget
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	for {
		entries, bytes := len(snap.entries), snap.bytes
		var oldest *snapshot
		for _, existing := range s.snapshots {
			entries += len(existing.entries)
			bytes += existing.bytes
			if oldest == nil || existing.fetched.Before(oldest.fetched) {
				oldest = existing
			}
		}
		if len(s.snapshots) < maxSnapshots && entries <= maxEntries && bytes <= maxCacheBytes {
			break
		}
		if oldest == nil {
			return ErrBudget
		}
		s.remove(oldest.id)
	}
	s.snapshots[snap.id] = snap
	s.latest[snap.key] = snap.id
	return nil
}

type cursor struct {
	Snapshot string `json:"snapshot"`
	Query    string `json:"query"`
	Offset   int    `json:"offset"`
	Stale    bool   `json:"stale"`
}

func queryHash(q Query) string {
	q.Cursor = ""
	q.Refresh = false
	encoded, _ := json.Marshal(q)
	digest := sha256.Sum256(encoded)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (s *Service) encodeCursor(c cursor) string {
	payload, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) cursorPage(ctx context.Context, key string, q Query) (Page, error) {
	encoded, signature, ok := strings.Cut(q.Cursor, ".")
	if !ok {
		return Page{}, ErrSnapshotExpired
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Page{}, ErrSnapshotExpired
	}
	provided, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return Page{}, ErrSnapshotExpired
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return Page{}, ErrSnapshotExpired
	}
	var c cursor
	if json.Unmarshal(payload, &c) != nil || c.Query != queryHash(q) || c.Offset < 0 {
		return Page{}, ErrSnapshotExpired
	}
	s.mu.Lock()
	s.prune()
	snap := s.snapshots[c.Snapshot]
	s.mu.Unlock()
	if snap == nil || snap.key != key {
		return Page{}, ErrSnapshotExpired
	}
	return s.page(ctx, snap, q, c.Offset, c.Stale)
}

func (s *Service) page(ctx context.Context, snap *snapshot, q Query, offset int, stale bool) (Page, error) {
	select {
	case s.pageSlots <- struct{}{}:
		defer func() { <-s.pageSlots }()
	case <-ctx.Done():
		return Page{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	entries := selected(snap.entries, q)
	if offset > len(entries) {
		return Page{}, ErrSnapshotExpired
	}
	end := min(offset+q.Limit, len(entries))
	page := Page{Entries: entries[offset:end], SnapshotID: snap.id, Stale: stale, FetchedAt: snap.fetched, DirectoryID: snap.directoryID, ParentID: snap.parentID, TotalEntries: len(entries)}
	if end < len(entries) {
		next := s.encodeCursor(cursor{Snapshot: snap.id, Query: queryHash(q), Offset: end, Stale: stale})
		page.NextCursor = &next
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	return page, nil
}
