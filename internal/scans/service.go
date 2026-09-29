package scans

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/events"
	"github.com/themartz90/folderhug/internal/profiles"
)

var ErrSuperseded = fmt.Errorf("%w: a newer scan overlaps this folder; start a new scan", ErrInvalidState)

type Service struct {
	db        *sql.DB
	profiles  *profiles.Service
	auth      *auth.Service
	catalog   *catalog.Service
	fetcher   catalog.Fetcher
	events    *events.Hub
	mu        sync.Mutex
	active    map[string]context.CancelFunc
	origins   map[string]bool
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	wake      chan struct{}
	cursorKey []byte
	started   bool
}

func New(db *sql.DB, p *profiles.Service, a *auth.Service, c *catalog.Service, f catalog.Fetcher, h *events.Hub) *Service {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &Service{db: db, profiles: p, auth: a, catalog: c, fetcher: f, events: h, active: make(map[string]context.CancelFunc), origins: make(map[string]bool), wake: make(chan struct{}, 2), cursorKey: key}
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return ErrInvalidState
	}
	now := stamp(time.Now())
	if _, err := s.db.ExecContext(ctx, "UPDATE scan_jobs SET state='interrupted',active_since=NULL,updated_at=? WHERE state='running'", now); err != nil {
		return err
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.started = true
	for range 2 {
		s.wg.Add(1)
		go s.worker()
	}
	return nil
}
func (s *Service) Close() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *Service) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func stamp(t time.Time) string        { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func parseStamp(raw string) time.Time { t, _ := time.Parse(time.RFC3339Nano, raw); return t }

func (s *Service) authorized(ctx context.Context, p auth.Principal, profile, scope string) (auth.Principal, error) {
	p, err := s.auth.Refresh(ctx, p)
	if err != nil {
		return p, err
	}
	if !p.Can(profile, scope) {
		return p, ErrNotFound
	}
	return p, nil
}

func (s *Service) Create(ctx context.Context, p auth.Principal, profileID, key string, in Input) (Scan, error) {
	p, err := s.authorized(ctx, p, profileID, "scan:write")
	if err != nil {
		return Scan{}, err
	}
	in, err = normalizeInput(in)
	if err != nil {
		return Scan{}, err
	}
	if len(key) < 1 || len(key) > 128 {
		return Scan{}, ErrInvalid
	}
	for _, ch := range key {
		if ch < 32 || ch > 126 {
			return Scan{}, ErrInvalid
		}
	}
	profile, err := s.profiles.Get(ctx, profileID, profiles.LeaseID(p))
	if err != nil {
		return Scan{}, err
	}
	encoded, _ := json.Marshal(in)
	sum := sha256.Sum256(encoded)
	hash := base64.RawURLEncoding.EncodeToString(sum[:])
	var replayHash, replayResponse string
	replayErr := s.db.QueryRowContext(ctx, "SELECT payload_hash,response FROM scan_idempotency WHERE creator_id=? AND profile_id=? AND key=? AND expires_at>?", p.CreatorID, profileID, key, time.Now().Unix()).Scan(&replayHash, &replayResponse)
	if replayErr == nil {
		if replayHash != hash {
			return Scan{}, ErrIdempotencyConflict
		}
		var replay Scan
		err := json.Unmarshal([]byte(replayResponse), &replay)
		return replay, err
	}
	if !errors.Is(replayErr, sql.ErrNoRows) {
		return Scan{}, replayErr
	}
	entry, err := s.catalog.Entry(ctx, profileID, profiles.LeaseID(p), in.DirectoryID)
	if err != nil {
		return Scan{}, err
	}
	if entry.Kind != "directory" {
		return Scan{}, ErrInvalid
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Scan{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "DELETE FROM scan_idempotency WHERE expires_at<=?", now.Unix())
	if err != nil {
		return Scan{}, err
	}
	var previousHash, response string
	err = tx.QueryRowContext(ctx, "SELECT payload_hash,response FROM scan_idempotency WHERE creator_id=? AND profile_id=? AND key=? AND expires_at>?", p.CreatorID, profileID, key, now.Unix()).Scan(&previousHash, &response)
	if err == nil {
		if previousHash != hash {
			return Scan{}, ErrIdempotencyConflict
		}
		var result Scan
		err = json.Unmarshal([]byte(response), &result)
		return result, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Scan{}, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM scan_jobs WHERE profile_id=? AND state IN ('queued','running','paused','waiting_for_credentials','interrupted')", profileID).Scan(&active); err != nil {
		return Scan{}, err
	}
	if active != 0 {
		return Scan{}, ErrInvalidState
	}
	u, _ := url.Parse(profile.RootURL)
	result := Scan{ID: rand.Text(), ProfileID: profileID, DirectoryID: in.DirectoryID, State: "queued", CreatedAt: now, UpdatedAt: now, Coverage: Coverage{ProfileID: profileID, DirectoryID: in.DirectoryID, State: "partial", LastScanAt: &now}}
	decoded, _ := url.PathUnescape(entry.EscapedPath)
	_, err = tx.ExecContext(ctx, `INSERT INTO scan_jobs(id,profile_id,generation,creator_id,lease_id,owner,origin,directory_id,root_path,root_key,input,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,'queued',?,?)`, result.ID, profileID, profile.Generation, p.CreatorID, profiles.LeaseID(p), p.Owner, u.Scheme+"://"+u.Host, in.DirectoryID, entry.EscapedPath, decoded, string(encoded), stamp(now), stamp(now))
	if err != nil {
		return Scan{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO scan_frontier(scan_id,path_key,escaped_path,depth) VALUES(?,?,?,0)", result.ID, decoded, entry.EscapedPath)
	if err != nil {
		return Scan{}, err
	}
	responseBytes, _ := json.Marshal(result)
	_, err = tx.ExecContext(ctx, "INSERT INTO scan_idempotency VALUES(?,?,?,?,?,?)", p.CreatorID, profileID, key, hash, string(responseBytes), now.Add(24*time.Hour).Unix())
	if err != nil {
		return Scan{}, err
	}
	if err = tx.Commit(); err != nil {
		return Scan{}, err
	}
	s.publish(result, p.CreatorID)
	s.notify()
	return result, nil
}

const jobColumns = `id,profile_id,generation,creator_id,lease_id,owner,origin,directory_id,root_path,input,state,directories_visited,files_found,directories_failed,directories_skipped,entries_seen,elapsed_seconds,partial,created_at,updated_at,error`

type rowScanner interface{ Scan(...any) error }

func decodeJob(row rowScanner) (job, error) {
	var j job
	var input, created, updated string
	var failure sql.NullString
	err := row.Scan(&j.ID, &j.ProfileID, &j.Generation, &j.CreatorID, &j.LeaseID, &j.Owner, &j.Origin, &j.DirectoryID, &j.RootPath, &input, &j.State, &j.DirectoriesVisited, &j.FilesFound, &j.DirectoriesFailed, &j.DirectoriesSkipped, &j.EntriesSeen, &j.ElapsedSeconds, &j.Partial, &created, &updated, &failure)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	if err = json.Unmarshal([]byte(input), &j.Input); err != nil {
		return j, err
	}
	j.CreatedAt = parseStamp(created)
	j.UpdatedAt = parseStamp(updated)
	if failure.Valid {
		if err = json.Unmarshal([]byte(failure.String), &j.Error); err != nil {
			return j, err
		}
	}
	state := "partial"
	if j.State == "completed" && !j.Partial {
		state = "complete"
	}
	last := j.UpdatedAt
	j.Coverage = Coverage{j.ProfileID, j.DirectoryID, state, &last}
	return j, nil
}
func (s *Service) load(ctx context.Context, id string) (job, error) {
	return decodeJob(s.db.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM scan_jobs WHERE id=?", id))
}
func (s *Service) Get(ctx context.Context, p auth.Principal, id string) (Scan, error) {
	j, err := s.load(ctx, id)
	if err != nil {
		return Scan{}, err
	}
	if _, err = s.authorized(ctx, p, j.ProfileID, "browse:read"); err != nil {
		return Scan{}, err
	}
	profile, err := s.profiles.Get(ctx, j.ProfileID, j.LeaseID)
	if err != nil {
		return Scan{}, err
	}
	if profile.Generation != j.Generation {
		j.Coverage.State = "stale"
	}
	return j.Scan, nil
}

func (s *Service) Action(ctx context.Context, p auth.Principal, id, action string) (Scan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.load(ctx, id)
	if err != nil {
		return Scan{}, err
	}
	p, err = s.authorized(ctx, p, j.ProfileID, "scan:write")
	if err != nil {
		return Scan{}, err
	}
	if !p.CanJob(j.ProfileID, j.CreatorID, "scan:write") {
		return Scan{}, ErrNotFound
	}
	state := ""
	switch action {
	case "pause":
		if j.State == "queued" || j.State == "running" {
			state = "paused"
		}
	case "cancel":
		if j.State != "completed" && j.State != "cancelled" && j.State != "failed" {
			state = "cancelled"
		}
	case "resume":
		if j.State == "paused" || j.State == "interrupted" || j.State == "waiting_for_credentials" || j.State == "partial" {
			state = "queued"
		}
	case "retry":
		if j.State == "failed" || j.State == "cancelled" {
			state = "queued"
		}
	default:
		return Scan{}, ErrInvalid
	}
	if state == "" {
		return Scan{}, ErrInvalidState
	}
	if state == "queued" {
		if j.Error != nil && j.Error.Code == "budget_exceeded" || j.EntriesSeen >= *j.Input.MaxEntries || j.ElapsedSeconds >= float64(*j.Input.MaxSeconds) {
			return Scan{}, ErrInvalidState
		}
		profile, err := s.profiles.Get(ctx, j.ProfileID, j.LeaseID)
		if err != nil {
			return Scan{}, err
		}
		if profile.Generation != j.Generation {
			return Scan{}, ErrInvalidState
		}
	}
	if state == "queued" {
		if _, running := s.active[id]; running {
			return Scan{}, ErrInvalidState
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Scan{}, err
	}
	defer tx.Rollback()
	if state == "queued" {
		// Job rows are retained until profile deletion. Their insertion order
		// survives service restarts and does not depend on the wall clock.
		// Even a filtered or partial later scan may have newer observations.
		var superseded bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM scan_jobs old JOIN scan_jobs newer
			ON newer.profile_id=old.profile_id AND newer.generation=old.generation AND newer.rowid>old.rowid
			WHERE old.id=? AND (substr(newer.root_key,1,length(old.root_key))=old.root_key
			OR substr(old.root_key,1,length(newer.root_key))=newer.root_key))`, id).Scan(&superseded); err != nil {
			return Scan{}, err
		}
		if superseded {
			failure := &ApiError{Code: "scan_superseded", Message: safeMessage("scan_superseded"), RequestID: id}
			encoded, _ := json.Marshal(failure)
			if _, err = tx.ExecContext(ctx, "UPDATE scan_jobs SET error=? WHERE id=?", string(encoded), id); err != nil {
				return Scan{}, err
			}
			if err = tx.Commit(); err != nil {
				return Scan{}, err
			}
			j.Error = failure
			s.publish(j.Scan, j.CreatorID)
			return Scan{}, ErrSuperseded
		}
		var others int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM scan_jobs WHERE profile_id=? AND id<>? AND state IN ('queued','running','paused','waiting_for_credentials','interrupted')", j.ProfileID, id).Scan(&others); err != nil {
			return Scan{}, err
		}
		if others > 0 {
			return Scan{}, ErrInvalidState
		}
		// Retain whole-job entry and active-time limits when retrying the frontier.
		if _, err = tx.ExecContext(ctx, "UPDATE scan_frontier SET state='pending',error_code=NULL WHERE scan_id=? AND state='failed'", id); err != nil {
			return Scan{}, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE scan_jobs SET directories_failed=0,partial=(directories_skipped>0) WHERE id=?", id); err != nil {
			return Scan{}, err
		}
	}
	now := time.Now().UTC()
	lease := j.LeaseID
	if state == "queued" && p.CreatorID == j.CreatorID {
		lease = profiles.LeaseID(p)
	}
	result, err := tx.ExecContext(ctx, "UPDATE scan_jobs SET state=?,lease_id=?,updated_at=?,error=NULL WHERE id=? AND state=?", state, lease, stamp(now), id, j.State)
	if err != nil {
		return Scan{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return Scan{}, ErrInvalidState
	}
	if err = tx.Commit(); err != nil {
		return Scan{}, err
	}
	if cancel := s.active[id]; cancel != nil {
		cancel()
	}
	j.State = state
	j.UpdatedAt = now
	j.Error = nil
	s.publish(j.Scan, j.CreatorID)
	s.notify()
	return j.Scan, nil
}

func (s *Service) publish(scan Scan, creator string) {
	if s.events != nil {
		s.events.Publish(events.Event{Type: "scan", ObjectID: &scan.ID, ProfileID: &scan.ProfileID, State: scan.State}, creator)
	}
}

func (s *Service) jobPrincipal(ctx context.Context, j job) (auth.Principal, error) {
	if j.Owner {
		return auth.Principal{Owner: true, CreatorID: "owner"}, nil
	}
	p, err := s.auth.Refresh(ctx, auth.Principal{CreatorID: j.CreatorID})
	if err != nil {
		return p, err
	}
	if !p.CanJob(j.ProfileID, j.CreatorID, "scan:write") {
		return p, auth.ErrUnauthenticated
	}
	return p, nil
}

func selectedCategory(in Input, category string) bool {
	if len(in.Categories) == 0 {
		return true
	}
	for _, c := range in.Categories {
		if c == category {
			return true
		}
	}
	return false
}
func isExcluded(in Input, rootPath, path string) bool {
	decodedPath, _ := url.PathUnescape(path)
	decodedRoot, _ := url.PathUnescape(rootPath)
	decoded := strings.TrimPrefix(decodedPath, decodedRoot)
	for _, excluded := range in.ExcludedDirectories {
		prefix, _ := url.PathUnescape(excluded)
		if strings.HasPrefix(decoded, prefix) {
			return true
		}
	}
	return false
}
