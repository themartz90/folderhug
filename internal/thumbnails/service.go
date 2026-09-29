package thumbnails

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/events"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/upstream"
)

const readyTTL = 24 * time.Hour
const negativeTTL = 5 * time.Minute
const validationTTL = time.Minute
const maxRecords = 10000

type record struct {
	Thumbnail
	profileID, key, file, validator    string
	size, validated, expires, accessed int64
}
type task struct {
	id, profileID, entryID, key string
	principal                   auth.Principal
}

type Service struct {
	db              *sql.DB
	profiles        *profiles.Service
	auth            *auth.Service
	catalog         *catalog.Service
	fetcher         Fetcher
	events          *events.Hub
	video           VideoGenerator
	root            *os.Root
	secret          []byte
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	mu              sync.Mutex
	closed          bool
	queued          map[string]struct{}
	refs            map[string]int
	retired         map[string]int64
	bytes, maxBytes int64
	images, videos  chan task
	decodeSlots     chan struct{}
	now             func() time.Time
}

func New(db *sql.DB, profileService *profiles.Service, authentication *auth.Service, catalogService *catalog.Service, fetcher Fetcher, hub *events.Hub, config Config) (*Service, error) {
	if config.MaxBytes <= 0 {
		config.MaxBytes = 1 << 30
	}
	if err := os.MkdirAll(config.CacheDir, 0700); err != nil {
		return nil, err
	}
	parent, err := os.OpenRoot(config.CacheDir)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if err := parent.Mkdir("thumbnails", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := parent.Lstat("thumbnails")
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrInvalid
	}
	root, err := parent.OpenRoot("thumbnails")
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalid
	}
	if err := clearOldFiles(root); err != nil {
		root.Close()
		return nil, err
	}
	if _, err := db.Exec(`DELETE FROM thumbnails`); err != nil {
		root.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	secret := make([]byte, 32)
	rand.Read(secret)
	s := &Service{db: db, profiles: profileService, auth: authentication, catalog: catalogService, fetcher: fetcher, events: hub, video: config.Video, root: root, secret: secret, ctx: ctx, cancel: cancel, queued: map[string]struct{}{}, refs: map[string]int{}, retired: map[string]int64{}, maxBytes: config.MaxBytes, images: make(chan task, 100), videos: make(chan task, 100), decodeSlots: make(chan struct{}, 2), now: time.Now}
	for i := 0; i < 2; i++ {
		s.wg.Add(1)
		go s.worker(s.images)
	}
	if s.video != nil {
		s.wg.Add(1)
		go s.worker(s.videos)
	}
	return s, nil
}

func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
	return s.root.Close()
}

func (s *Service) resolve(ctx context.Context, p auth.Principal, profileID, entryID string) (profiles.Profile, listing.Entry, string, error) {
	if !p.Can(profileID, "content:read") {
		return profiles.Profile{}, listing.Entry{}, "", ErrNotFound
	}
	profile, credentials, active, cancel, err := s.profiles.Access(ctx, profileID, profiles.LeaseID(p))
	if err != nil {
		return profile, listing.Entry{}, "", err
	}
	defer cancel()
	entry, err := s.catalog.Entry(active, profileID, profiles.LeaseID(p), entryID)
	if err != nil {
		return profile, entry, "", err
	}
	if listing.ID(profileID, profile.Generation, entry.EscapedPath) != entry.ID {
		return profile, entry, "", ErrNotFound
	}
	encoded, _ := json.Marshal(struct {
		ProfileID   string
		Generation  int
		Previews    bool
		Entry       listing.Entry
		Lease       string
		Credentials *upstream.Credentials
	}{profileID, profile.Generation, profile.Previews, entry, profiles.LeaseID(p), credentials})
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(encoded)
	return profile, entry, hex.EncodeToString(mac.Sum(nil)), nil
}

func (s *Service) Request(ctx context.Context, p auth.Principal, profileID string, entryIDs []string) ([]Thumbnail, error) {
	if len(entryIDs) == 0 || len(entryIDs) > 50 {
		return nil, ErrInvalid
	}
	var err error
	p, err = s.auth.Refresh(ctx, p)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	type resolved struct {
		profile profiles.Profile
		entry   listing.Entry
		key     string
	}
	inputs := make([]resolved, 0, len(entryIDs))
	for _, id := range entryIDs {
		if seen[id] {
			return nil, ErrInvalid
		}
		seen[id] = true
		profile, entry, key, err := s.resolve(ctx, p, profileID, id)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, resolved{profile, entry, key})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if err := s.evictLocked(0, ""); err != nil {
		return nil, err
	}
	result := make([]Thumbnail, 0, len(inputs))
	now := s.now()
	for _, input := range inputs {
		r, err := s.byKey(ctx, input.key)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		existing := err == nil
		if existing && (r.State == "pending" || r.expires > now.Unix() && (r.State != "ready" || r.validated+int64(validationTTL.Seconds()) > now.Unix())) {
			result = append(result, r.Thumbnail)
			continue
		}
		state := "pending"
		if !input.profile.Previews {
			state = "disabled"
		} else if input.entry.Kind != "file" || (input.entry.Category != "image" && input.entry.Category != "video") || input.entry.Category == "video" && s.video == nil {
			state = "unavailable"
		}
		if state == "pending" && len(s.queued) >= 100 {
			return nil, ErrBusy
		}
		if !existing {
			r = record{Thumbnail: Thumbnail{ID: rand.Text(), EntryID: input.entry.ID}, profileID: profileID, key: input.key}
		}
		r.State = state
		r.expires = now.Add(negativeTTL).Unix()
		r.accessed = now.Unix()
		if err := s.save(ctx, r); err != nil {
			return nil, err
		}
		if state == "pending" {
			s.queued[r.ID] = struct{}{}
			job := task{id: r.ID, profileID: profileID, entryID: input.entry.ID, key: input.key, principal: p}
			queue := s.images
			if input.entry.Category == "video" {
				queue = s.videos
			}
			queue <- job
		}
		result = append(result, r.Thumbnail)
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, p auth.Principal, id string) (Thumbnail, *File, error) {
	var err error
	p, err = s.auth.Refresh(ctx, p)
	if err != nil {
		return Thumbnail{}, nil, err
	}
	s.mu.Lock()
	r, err := s.byID(ctx, id)
	s.mu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		return Thumbnail{}, nil, ErrNotFound
	}
	if err != nil {
		return Thumbnail{}, nil, err
	}
	profile, _, key, err := s.resolve(ctx, p, r.profileID, r.EntryID)
	if err != nil {
		return Thumbnail{}, nil, err
	}
	if !profile.Previews {
		return Thumbnail{ID: r.ID, EntryID: r.EntryID, State: "disabled"}, nil, nil
	}
	if key != r.key {
		return Thumbnail{}, nil, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Thumbnail{}, nil, ErrClosed
	}
	r, err = s.byID(ctx, id)
	if err != nil {
		return Thumbnail{}, nil, ErrNotFound
	}
	if r.State != "ready" {
		return r.Thumbnail, nil, nil
	}
	if r.expires <= s.now().Unix() {
		return Thumbnail{ID: r.ID, EntryID: r.EntryID, State: "unavailable"}, nil, nil
	}
	file, err := openCacheFile(s.root, r.file)
	if err != nil {
		return Thumbnail{}, nil, ErrNotFound
	}
	s.refs[r.file]++
	_, _ = s.db.ExecContext(ctx, `UPDATE thumbnails SET accessed_at=? WHERE id=?`, s.now().Unix(), id)
	return r.Thumbnail, &File{File: file, release: func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.refs[r.file]--
		if s.refs[r.file] == 0 {
			delete(s.refs, r.file)
			if size, ok := s.retired[r.file]; ok {
				s.removeFileLocked(r.file, size)
			}
		}
	}}, nil
}

func (s *Service) ClearProfile(ctx context.Context, profileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id,file_name,size_bytes FROM thumbnails WHERE profile_id=?`, profileID)
	if err != nil {
		return err
	}
	type removed struct {
		id, file string
		size     int64
	}
	items := []removed{}
	for rows.Next() {
		var item removed
		if err := rows.Scan(&item.id, &item.file, &item.size); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM thumbnails WHERE profile_id=?`, profileID); err != nil {
		return err
	}
	for _, item := range items {
		s.removeFileLocked(item.file, item.size)
	}
	return nil
}

const columns = `id,entry_id,state,profile_id,cache_key,file_name,size_bytes,validator,validated_at,expires_at,accessed_at`

func scanRecord(row interface{ Scan(...any) error }) (record, error) {
	var r record
	err := row.Scan(&r.ID, &r.EntryID, &r.State, &r.profileID, &r.key, &r.file, &r.size, &r.validator, &r.validated, &r.expires, &r.accessed)
	return r, err
}
func (s *Service) byID(ctx context.Context, id string) (record, error) {
	return scanRecord(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM thumbnails WHERE id=?`, id))
}
func (s *Service) byKey(ctx context.Context, key string) (record, error) {
	return scanRecord(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM thumbnails WHERE cache_key=?`, key))
}
func (s *Service) save(ctx context.Context, r record) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO thumbnails(id,entry_id,state,profile_id,cache_key,file_name,size_bytes,validator,validated_at,expires_at,accessed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state,file_name=excluded.file_name,size_bytes=excluded.size_bytes,validator=excluded.validator,validated_at=excluded.validated_at,expires_at=excluded.expires_at,accessed_at=excluded.accessed_at`, r.ID, r.EntryID, r.State, r.profileID, r.key, r.file, r.size, r.validator, r.validated, r.expires, r.accessed)
	return err
}

func (s *Service) worker(queue <-chan task) {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case job := <-queue:
			s.process(job)
		}
	}
}

func (s *Service) process(job task) {
	ctx, cancel := context.WithTimeout(s.ctx, 35*time.Second)
	defer cancel()
	p, err := s.auth.Refresh(ctx, job.principal)
	if err != nil {
		s.finish(job, nil, "", false)
		return
	}
	profile, entry, key, err := s.resolve(ctx, p, job.profileID, job.entryID)
	if err != nil || key != job.key || !profile.Previews {
		s.finish(job, nil, "", false)
		return
	}
	s.mu.Lock()
	old, err := s.byID(ctx, job.id)
	s.mu.Unlock()
	if err != nil {
		s.finish(job, nil, "", false)
		return
	}
	var body []byte
	validator := ""
	reused := false
	if entry.Category == "video" {
		if s.video != nil {
			body, err = s.video.Generate(ctx, VideoRequest{ProfileID: profile.ID, EntryID: entry.ID, Generation: profile.Generation, Principal: p})
			if err == nil && (len(body) < 2 || body[0] != 0xff || body[1] != 0xd8) {
				err = ErrImage
			}
		} else {
			err = ErrImage
		}
	} else {
		body, validator, reused, err = s.fetchImage(ctx, p, profile, entry, old)
	}
	if err == nil && !reused {
		select {
		case s.decodeSlots <- struct{}{}:
			body, err = raster(body)
			<-s.decodeSlots
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	_, _, currentKey, currentErr := s.resolve(ctx, p, job.profileID, job.entryID)
	if err != nil || currentErr != nil || currentKey != job.key || ctx.Err() != nil {
		s.finish(job, nil, "", false)
		return
	}
	s.finish(job, body, validator, reused)
}

func (s *Service) fetchImage(ctx context.Context, p auth.Principal, profile profiles.Profile, entry listing.Entry, old record) ([]byte, string, bool, error) {
	current, credentials, active, cancel, err := s.profiles.Access(ctx, profile.ID, profiles.LeaseID(p))
	if err != nil {
		return nil, "", false, err
	}
	defer cancel()
	if current.Generation != profile.Generation {
		return nil, "", false, ErrNotFound
	}
	root, err := upstream.NewRoot(current.RootURL, current.AllowHTTP)
	if err != nil {
		return nil, "", false, err
	}
	options := upstream.Options{Credentials: credentials, MaxBytes: maxInput, Timeout: 30 * time.Second}
	if old.file != "" && old.validator != "" {
		response, err := s.fetcher.Do(active, root, http.MethodHead, entry.EscapedPath, options)
		if err == nil {
			value := responseValidator(response)
			response.Body.Close()
			if response.StatusCode == 200 && value != "" && value == old.validator {
				return nil, value, true, nil
			}
		}
	}
	response, err := s.fetcher.Do(active, root, http.MethodGet, entry.EscapedPath, options)
	if err != nil {
		return nil, "", false, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.ContentLength > maxInput || response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity" {
		return nil, "", false, ErrImage
	}
	body, err := readImage(response.Body)
	return body, responseValidator(response), false, err
}

func responseValidator(response *http.Response) string {
	etag := response.Header.Get("ETag")
	if len(etag) > 512 {
		return ""
	}
	if etag != "" {
		return "etag:" + etag
	}
	modified, length := response.Header.Get("Last-Modified"), response.Header.Get("Content-Length")
	if modified != "" && len(modified) <= 128 && length != "" && len(length) <= 24 {
		return "modified:" + modified + ":" + length
	}
	return ""
}

func (s *Service) finish(job task, body []byte, validator string, reused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.queued, job.id)
	if s.closed {
		return
	}
	r, err := s.byID(s.ctx, job.id)
	if err != nil || r.key != job.key {
		return
	}
	oldFile, oldSize := r.file, r.size
	r.State = "unavailable"
	r.expires = s.now().Add(negativeTTL).Unix()
	r.accessed = s.now().Unix()
	if reused && r.file != "" {
		r.State = "ready"
	} else if len(body) > 0 {
		if s.evictLocked(int64(len(body)), r.ID) == nil {
			if name, err := writeCacheFile(s.root, body); err == nil {
				r.file = name
				r.size = int64(len(body))
				s.bytes += r.size
				r.State = "ready"
			}
		}
	}
	if r.State == "ready" {
		r.validator = validator
		r.validated = s.now().Unix()
		r.expires = s.now().Add(readyTTL).Unix()
	} else {
		r.file = ""
		r.size = 0
		r.validator = ""
	}
	if err := s.save(s.ctx, r); err != nil {
		if r.file != oldFile {
			s.removeFileLocked(r.file, r.size)
		}
		return
	}
	if oldFile != r.file {
		s.removeFileLocked(oldFile, oldSize)
	}
	if s.events != nil {
		s.events.Publish(events.Event{Type: "thumbnail", ObjectID: &r.ID, ProfileID: &r.profileID, State: r.State}, job.principal.CreatorID)
	}
}

func managedName(name string) bool {
	if !strings.HasPrefix(name, "thumbnail-") {
		return false
	}
	name = strings.TrimPrefix(name, "thumbnail-")
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".jpg"), ".tmp")
	if base == name || len(base) < 26 || len(base) > 64 {
		return false
	}
	for _, c := range base {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
