package downloads

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/content"
	"github.com/themartz90/folderhug/internal/downloadfs"
	"github.com/themartz90/folderhug/internal/events"
	"github.com/themartz90/folderhug/internal/profiles"
)

type Service struct {
	db           *sql.DB
	profiles     *profiles.Service
	auth         *auth.Service
	catalog      *catalog.Service
	fetcher      content.Fetcher
	hub          *events.Hub
	files        *downloadfs.Store
	mounts       map[string]MountedRoot
	stageMount   func(context.Context, job) (*downloadfs.PreparedMount, error)
	limits       Limits
	op           sync.Mutex
	active       map[string]context.CancelCauseFunc
	cancel       context.CancelFunc
	wake         chan struct{}
	wg           sync.WaitGroup
	budgetMu     sync.Mutex
	reservations map[string]*reservation
	rateMu       sync.Mutex
	rates        map[string]rateStart
}

type rateStart struct {
	at    time.Time
	bytes int64
}

func New(db *sql.DB, p *profiles.Service, a *auth.Service, c *catalog.Service, f content.Fetcher, h *events.Hub, files *downloadfs.Store, limits Limits) *Service {
	s := &Service{db: db, profiles: p, auth: a, catalog: c, fetcher: f, hub: h, files: files, mounts: make(map[string]MountedRoot), limits: defaults(limits), active: make(map[string]context.CancelCauseFunc), wake: make(chan struct{}, 2), reservations: make(map[string]*reservation), rates: make(map[string]rateStart)}
	s.stageMount = s.prepareMounted
	return s
}

func (s *Service) ConfigureMounts(roots []MountedRoot) {
	for _, root := range roots {
		s.mounts[root.ID] = root
	}
}

func openMountedRoot(root MountedRoot) (*downloadfs.Mount, error) {
	if root.GuardMount {
		return downloadfs.OpenMountChecked(root.Path, root.ProtectedPaths)
	}
	return downloadfs.OpenMount(root.Path)
}

func (s *Service) Destinations(owner bool) ([]Destination, error) {
	available, err := s.files.AvailableBytes()
	if err != nil {
		return nil, err
	}
	items := []Destination{{ID: "server", Label: "Server downloads", AvailableBytes: available, Available: true}}
	if owner {
		for _, root := range s.mounts {
			item := Destination{ID: root.ID, Label: root.Label, Browseable: true}
			mount, e := openMountedRoot(root)
			if e == nil {
				item.Available = true
				item.AvailableBytes, _ = mount.AvailableBytes()
				mount.Close()
			}
			items = append(items, item)
		}
		sort.Slice(items[1:], func(i, j int) bool { return items[i+1].Label < items[j+1].Label })
	}
	return items, nil
}

func (s *Service) Folders(owner bool, id, path string) (FolderPage, error) {
	root, ok := s.mounts[id]
	if !owner || !ok {
		return FolderPage{}, ErrNotFound
	}
	if !downloadfs.ValidFolder(path) {
		return FolderPage{}, ErrInvalid
	}
	mount, err := openMountedRoot(root)
	if err != nil {
		return FolderPage{}, ErrDestinationUnavailable
	}
	defer mount.Close()
	items, err := mount.Folders(path)
	if err != nil {
		if os.IsNotExist(err) {
			return FolderPage{}, ErrDestinationUnavailable
		}
		if errors.Is(err, downloadfs.ErrUnsafe) {
			return FolderPage{}, ErrInvalid
		}
		return FolderPage{}, err
	}
	folders := make([]Folder, len(items))
	for i, item := range items {
		folders[i] = Folder{item.Name, item.Path}
	}
	return FolderPage{id, path, folders}, nil
}

func (s *Service) Start(ctx context.Context) error {
	s.op.Lock()
	defer s.op.Unlock()
	if s.cancel != nil {
		return ErrState
	}
	if err := s.reconcile(ctx); err != nil {
		return err
	}
	ctx, s.cancel = context.WithCancel(ctx)
	for range 2 {
		s.wg.Add(1)
		go s.worker(ctx)
	}
	s.signal()
	return nil
}

func (s *Service) Close() {
	s.op.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.op.Unlock()
	s.wg.Wait()
}
func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) Create(ctx context.Context, p auth.Principal, in Input, key string) (Download, error) {
	p, err := s.auth.Refresh(ctx, p)
	if err != nil {
		return Download{}, err
	}
	if !p.Can(in.ProfileID, "downloads:write") {
		return Download{}, ErrNotFound
	}
	connections := 1
	if in.Connections != nil {
		connections = *in.Connections
	}
	if (connections != 1 && connections != 2 && connections != 4) || (in.BytesPerSecond != nil && *in.BytesPerSecond < 1024) || !validKey(key) || !downloadfs.ValidFolder(in.DestinationPath) {
		return Download{}, ErrInvalid
	}
	if in.DestinationID == "server" {
		if in.DestinationPath != "" {
			return Download{}, ErrInvalid
		}
	} else {
		root, ok := s.mounts[in.DestinationID]
		if !p.Owner || !ok {
			return Download{}, ErrNotFound
		}
		mount, e := openMountedRoot(root)
		if e != nil {
			return Download{}, ErrDestinationUnavailable
		}
		e = mount.ValidateFolder(in.DestinationPath)
		mount.Close()
		if e != nil {
			if os.IsNotExist(e) {
				return Download{}, ErrDestinationUnavailable
			}
			if errors.Is(e, downloadfs.ErrUnsafe) {
				return Download{}, ErrInvalid
			}
			return Download{}, e
		}
	}
	in.Connections = &connections
	payload, _ := json.Marshal(in)
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	s.op.Lock()
	defer s.op.Unlock()
	_, err = s.db.ExecContext(ctx, `DELETE FROM download_idempotency WHERE expires_at<=?`, time.Now().Unix())
	if err != nil {
		return Download{}, err
	}
	var existing, prior string
	var response sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT job_id,payload_hash,response FROM download_idempotency WHERE creator_id=? AND key=?`, p.CreatorID, key).Scan(&existing, &prior, &response)
	if err == nil {
		if prior != hash {
			return Download{}, ErrIdempotency
		}
		j, err := s.load(ctx, existing)
		if err != nil {
			return Download{}, err
		}
		if !visible(p, j, "downloads:write") {
			return Download{}, ErrNotFound
		}
		if response.Valid {
			var original Download
			if err := json.Unmarshal([]byte(response.String), &original); err != nil {
				return Download{}, err
			}
			return original, nil
		}
		return initialDownload(j.Download), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Download{}, err
	}
	lease := profiles.LeaseID(p)
	profile, err := s.profiles.Get(ctx, in.ProfileID, lease)
	if err != nil {
		return Download{}, err
	}
	if connections > profile.Connections {
		return Download{}, ErrInvalid
	}
	entry, err := s.catalog.Entry(ctx, in.ProfileID, lease, in.EntryID)
	if err != nil {
		return Download{}, err
	}
	if entry.Kind != "file" {
		return Download{}, ErrInvalid
	}
	if p.Owner && profile.CredentialState != "temporary" {
		lease = ""
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	id := rand.Text()
	bps := int64(0)
	if in.BytesPerSecond != nil {
		bps = *in.BytesPerSecond
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Download{}, err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_jobs WHERE state IN ('queued','running','paused','interrupted','waiting_for_credentials')`).Scan(&count); err != nil {
		return Download{}, err
	}
	if count >= 1000 {
		return Download{}, ErrBusy
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO download_jobs(id,profile_id,generation,creator_id,lease_id,owner,entry_id,escaped_path,name,destination_id,destination_path,requested_connections,bps_limit,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, in.ProfileID, profile.Generation, p.CreatorID, lease, p.Owner, in.EntryID, entry.EscapedPath, downloadfs.SafeName(entry.Name), in.DestinationID, in.DestinationPath, connections, bps, "queued", now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return Download{}, err
	}
	original := initialDownload(Download{ID: id, ProfileID: &in.ProfileID, EntryID: in.EntryID, Name: downloadfs.SafeName(entry.Name), DestinationID: in.DestinationID, DestinationPath: in.DestinationPath, CreatedAt: now})
	encoded, _ := json.Marshal(original)
	_, err = tx.ExecContext(ctx, `INSERT INTO download_idempotency(creator_id,key,payload_hash,job_id,expires_at,response) VALUES(?,?,?,?,?,?)`, p.CreatorID, key, hash, id, now.Add(24*time.Hour).Unix(), string(encoded))
	if err != nil {
		return Download{}, err
	}
	if err = tx.Commit(); err != nil {
		return Download{}, err
	}
	j, err := s.load(ctx, id)
	if err == nil {
		s.publish(j)
		s.signal()
	}
	return original, err
}

func initialDownload(j Download) Download {
	return Download{ID: j.ID, ProfileID: j.ProfileID, EntryID: j.EntryID, Name: j.Name, DestinationID: j.DestinationID, DestinationPath: j.DestinationPath, State: "queued", Connections: 1, CreatedAt: j.CreatedAt, UpdatedAt: j.CreatedAt}
}

func validKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}
func visible(p auth.Principal, j job, scope string) bool {
	if p.Owner {
		return p.Can("", scope)
	}
	return j.ProfileID != nil && p.CanJob(*j.ProfileID, j.creator, scope)
}

func (s *Service) Get(ctx context.Context, p auth.Principal, id string) (Download, error) {
	j, err := s.authorized(ctx, p, id, "downloads:write")
	return j.Download, err
}
func (s *Service) authorized(ctx context.Context, p auth.Principal, id, scope string) (job, error) {
	p, err := s.auth.Refresh(ctx, p)
	if err != nil {
		return job{}, err
	}
	j, err := s.load(ctx, id)
	if err != nil {
		return j, err
	}
	if !visible(p, j, scope) {
		return job{}, ErrNotFound
	}
	return j, nil
}

type cursor struct {
	Created   int64
	ID, State string
}

func (s *Service) List(ctx context.Context, p auth.Principal, q Query) (Page, error) {
	p, err := s.auth.Refresh(ctx, p)
	if err != nil {
		return Page{}, err
	}
	if q.Limit == 0 {
		q.Limit = 200
	}
	if q.Limit < 1 || q.Limit > 1000 || !validState(q.State, true) {
		return Page{}, ErrInvalid
	}
	where := "1=1"
	args := []any{}
	if !p.Owner {
		ids := []string{}
		for _, id := range p.ProfileIDs {
			if p.Can(id, "downloads:write") {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return Page{Downloads: []Download{}}, nil
		}
		where += " AND creator_id=? AND profile_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
		args = append(args, p.CreatorID)
		for _, id := range ids {
			args = append(args, id)
		}
	}
	if q.State != "" {
		where += " AND state=?"
		args = append(args, q.State)
	}
	if q.Cursor != "" {
		var cur cursor
		b, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		if e != nil || json.Unmarshal(b, &cur) != nil || cur.State != q.State {
			return Page{}, ErrInvalid
		}
		where += " AND (created_at<? OR (created_at=? AND id<?))"
		args = append(args, cur.Created, cur.Created, cur.ID)
	}
	args = append(args, q.Limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM download_jobs WHERE `+where+` ORDER BY created_at DESC,id DESC LIMIT ?`, args...)
	if err != nil {
		return Page{}, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return Page{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Page{}, err
	}
	page := Page{Downloads: []Download{}}
	for i, id := range ids {
		if i == q.Limit {
			last, err := s.load(ctx, ids[i-1])
			if err != nil {
				return Page{}, err
			}
			b, _ := json.Marshal(cursor{last.CreatedAt.UnixMilli(), last.ID, q.State})
			encoded := base64.RawURLEncoding.EncodeToString(b)
			page.NextCursor = &encoded
			break
		}
		j, err := s.load(ctx, id)
		if err != nil {
			return Page{}, err
		}
		if visible(p, j, "downloads:write") {
			page.Downloads = append(page.Downloads, j.Download)
		}
	}
	return page, nil
}

func validState(state string, empty bool) bool {
	switch state {
	case "queued", "running", "paused", "waiting_for_credentials", "interrupted", "completed", "cancelled", "failed":
		return true
	case "":
		return empty
	}
	return false
}

func (s *Service) Action(ctx context.Context, p auth.Principal, id, action string, allowRestart bool) (Download, error) {
	p, err := s.auth.Refresh(ctx, p)
	if err != nil {
		return Download{}, err
	}
	s.op.Lock()
	defer s.op.Unlock()
	j, err := s.load(ctx, id)
	if err != nil {
		return Download{}, err
	}
	if !visible(p, j, "downloads:write") {
		return Download{}, ErrNotFound
	}
	state := ""
	reset := false
	switch action {
	case "pause":
		if j.State != "queued" && j.State != "running" {
			return Download{}, ErrState
		}
		state = "paused"
	case "cancel":
		if j.State == "completed" || j.State == "cancelled" {
			return Download{}, ErrState
		}
		state = "cancelled"
	case "resume":
		if j.State != "paused" && j.State != "interrupted" && j.State != "waiting_for_credentials" {
			return Download{}, ErrState
		}
		if s.active[id] != nil {
			return Download{}, ErrState
		}
		if !j.publishing && (j.restart || (j.TransferredBytes > 0 && (!strongETag(j.etag) || j.TotalBytes == nil || !j.ranges))) {
			if !allowRestart {
				return Download{}, ErrRestart
			}
			reset = true
		}
		state = "queued"
	case "retry":
		if j.State != "failed" && j.State != "cancelled" {
			return Download{}, ErrState
		}
		if s.active[id] != nil {
			return Download{}, ErrState
		}
		// A failed destination copy can reuse the validated internal file.
		reset = !(j.DestinationID != "server" && j.publishing && !j.restart)
		state = "queued"
	default:
		return Download{}, ErrInvalid
	}
	if state == "queued" {
		if j.State == "failed" || j.State == "cancelled" {
			var count int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_jobs WHERE state IN ('queued','running','paused','interrupted','waiting_for_credentials')`).Scan(&count); err != nil {
				return Download{}, err
			}
			if count >= 1000 {
				return Download{}, ErrBusy
			}
		}
		if j.ProfileID == nil {
			return Download{}, ErrNotFound
		}
		profile, err := s.profiles.Get(ctx, *j.ProfileID, profiles.LeaseID(p))
		if err != nil {
			return Download{}, err
		}
		if profile.Generation != j.generation {
			return Download{}, ErrNotFound
		}
		if reset {
			if err := s.reset(ctx, j); err != nil {
				return Download{}, err
			}
		}
		// The owner explicitly resuming a job assumes responsibility for it.
		if p.Owner {
			j.owner = true
			j.creator = "owner"
		}
		j.lease = profiles.LeaseID(p)
		if j.owner && profile.CredentialState != "temporary" {
			j.lease = ""
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE download_jobs SET state=?,creator_id=?,owner=?,lease_id=?,error=NULL,updated_at=? WHERE id=?`, state, j.creator, j.owner, j.lease, time.Now().UnixMilli(), id)
	if err != nil {
		return Download{}, err
	}
	if cancel := s.active[id]; cancel != nil {
		cancel(context.Canceled)
	}
	j, err = s.load(ctx, id)
	if err == nil {
		s.publish(j)
		s.signal()
	}
	return j.Download, err
}

func (s *Service) completedJob(ctx context.Context, p auth.Principal, id string) (job, error) {
	j, err := s.authorized(ctx, p, id, "content:read")
	if err != nil {
		return job{}, err
	}
	if j.State != "completed" || !j.FileAvailable {
		return job{}, ErrNotFound
	}
	return j, nil
}

// CheckCompletedAccess reloads the principal and job without reopening the file.
func (s *Service) CheckCompletedAccess(ctx context.Context, p auth.Principal, id string) error {
	_, err := s.completedJob(ctx, p, id)
	return err
}

func (s *Service) Completed(ctx context.Context, p auth.Principal, id string) (*os.File, Download, error) {
	j, err := s.completedJob(ctx, p, id)
	if err != nil {
		return nil, Download{}, err
	}
	if j.DestinationID != "server" {
		root, ok := s.mounts[j.DestinationID]
		if !ok || j.mountedName == "" {
			return nil, Download{}, ErrDestinationUnavailable
		}
		mount, e := openMountedRoot(root)
		if e != nil {
			return nil, Download{}, ErrDestinationUnavailable
		}
		f, e := mount.OpenPublished(j.DestinationPath, j.mountedName, j.ID)
		mount.Close()
		if e != nil {
			return nil, Download{}, ErrDestinationUnavailable
		}
		stat, e := f.Stat()
		if e != nil || stat.Size() != j.TransferredBytes {
			f.Close()
			return nil, Download{}, ErrDestinationUnavailable
		}
		return f, j.Download, nil
	}
	dir, err := s.files.OpenJob(j.ID, false)
	if err != nil {
		return nil, Download{}, ErrNotFound
	}
	defer dir.Close()
	f, err := dir.OpenCompleted(j.Name)
	if err != nil {
		return nil, Download{}, ErrNotFound
	}
	stat, err := f.Stat()
	if err != nil || stat.Size() != j.TransferredBytes {
		f.Close()
		return nil, Download{}, ErrNotFound
	}
	return f, j.Download, nil
}

func (s *Service) DeletePartial(ctx context.Context, p auth.Principal, id string) error {
	s.op.Lock()
	defer s.op.Unlock()
	j, err := s.authorized(ctx, p, id, "downloads:write")
	if err != nil {
		return err
	}
	if (j.State != "failed" && j.State != "cancelled") || s.active[id] != nil {
		return ErrState
	}
	return s.reset(ctx, j)
}

func (s *Service) DeleteCompleted(ctx context.Context, p auth.Principal, id string) error {
	p, err := s.auth.Refresh(ctx, p)
	if err != nil {
		return err
	}
	if !p.Owner {
		return ErrNotFound
	}
	s.op.Lock()
	defer s.op.Unlock()
	j, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if j.State != "completed" || !j.FileAvailable {
		return ErrState
	}
	if j.DestinationID != "server" {
		return ErrState
	}
	dir, err := s.files.OpenJob(id, false)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = dir.RemoveCompleted(j.Name); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE download_jobs SET file_available=0,updated_at=? WHERE id=?`, time.Now().UnixMilli(), id)
	return err
}

func (s *Service) reset(ctx context.Context, j job) error {
	if j.DestinationID != "server" && j.publishing {
		root, ok := s.mounts[j.DestinationID]
		if !ok {
			return ErrDestinationUnavailable
		}
		mount, err := openMountedRoot(root)
		if err != nil {
			return ErrDestinationUnavailable
		}
		err = mount.Cleanup(j.DestinationPath, j.ID)
		mount.Close()
		if err != nil {
			return mountedError(err)
		}
	}
	dir, err := s.files.OpenJob(j.ID, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	if j.DestinationID != "server" && !j.FileAvailable {
		if err = dir.RemoveCompleted(j.Name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err = dir.RemovePart(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM download_segments WHERE job_id=?`, j.ID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE download_jobs SET transferred=0,total_bytes=NULL,etag='',range_verified=0,restart_required=0,publishing=0,checksum='',connections=1,fallback_reason=NULL WHERE id=?`, j.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) load(ctx context.Context, id string) (job, error) {
	var j job
	var profile sql.NullString
	var total sql.NullInt64
	var fallback, errorJSON sql.NullString
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id,profile_id,generation,creator_id,lease_id,owner,entry_id,escaped_path,name,destination_id,destination_path,mounted_name,requested_connections,connections,bps_limit,state,transferred,total_bytes,etag,range_verified,restart_required,fallback_reason,file_available,publishing,checksum,error,created_at,updated_at FROM download_jobs WHERE id=?`, id).Scan(&j.ID, &profile, &j.generation, &j.creator, &j.lease, &j.owner, &j.EntryID, &j.path, &j.Name, &j.DestinationID, &j.DestinationPath, &j.mountedName, &j.requested, &j.Connections, &j.bps, &j.State, &j.TransferredBytes, &total, &j.etag, &j.ranges, &j.restart, &fallback, &j.FileAvailable, &j.publishing, &j.checksum, &errorJSON, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	if profile.Valid {
		j.ProfileID = &profile.String
	}
	if total.Valid {
		j.TotalBytes = &total.Int64
	}
	if fallback.Valid {
		j.FallbackReason = &fallback.String
	}
	if errorJSON.Valid {
		if err = json.Unmarshal([]byte(errorJSON.String), &j.Error); err != nil {
			return j, err
		}
	}
	if j.mountedName != "" {
		j.SavedName = &j.mountedName
	}
	if j.DestinationID == "server" && j.State == "completed" {
		j.SavedName = &j.Name
	}
	j.CreatedAt = time.UnixMilli(created).UTC()
	j.UpdatedAt = time.UnixMilli(updated).UTC()
	if j.State == "running" {
		s.rateMu.Lock()
		start, ok := s.rates[id]
		s.rateMu.Unlock()
		if ok && j.TransferredBytes > start.bytes {
			rate := float64(j.TransferredBytes-start.bytes) / time.Since(start.at).Seconds()
			j.BytesPerSecond = &rate
			if j.TotalBytes != nil {
				eta := float64(max(0, *j.TotalBytes-j.TransferredBytes)) / rate
				j.ETASeconds = &eta
			}
		}
	}
	return j, nil
}

func (s *Service) publish(j job) {
	if s.hub != nil {
		s.hub.Publish(events.Event{Type: "download", ObjectID: &j.ID, ProfileID: j.ProfileID, State: j.State}, j.creator)
	}
}
