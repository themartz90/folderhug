package downloads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/content"
	"github.com/themartz90/folderhug/internal/downloadfs"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/upstream"
)

func (s *Service) worker(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		j, active, err := s.claim(ctx)
		if err == nil && j.ID != "" {
			s.run(active, j)
			s.rateMu.Lock()
			delete(s.rates, j.ID)
			s.rateMu.Unlock()
			s.op.Lock()
			delete(s.active, j.ID)
			s.op.Unlock()
			s.signal()
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

func (s *Service) claim(ctx context.Context) (job, context.Context, error) {
	s.op.Lock()
	defer s.op.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM download_jobs WHERE state='queued' ORDER BY created_at,id LIMIT 3`)
	if err != nil {
		return job{}, nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return job{}, nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return job{}, nil, err
	}
	for _, id := range ids {
		if s.active[id] != nil {
			continue
		}
		result, err := s.db.ExecContext(ctx, `UPDATE download_jobs SET state='running',error=NULL,updated_at=? WHERE id=? AND state='queued'`, time.Now().UnixMilli(), id)
		if err != nil {
			return job{}, nil, err
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			continue
		}
		j, err := s.load(ctx, id)
		if err != nil {
			return job{}, nil, err
		}
		active, cancel := context.WithCancelCause(ctx)
		s.active[id] = cancel
		s.rateMu.Lock()
		s.rates[id] = rateStart{time.Now(), j.TransferredBytes}
		s.rateMu.Unlock()
		s.publish(j)
		return j, active, nil
	}
	return job{}, nil, nil
}

func (s *Service) checkAuthority(ctx context.Context, j job) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if j.ProfileID == nil {
		return errSourceChanged
	}
	if !j.owner {
		p, err := s.auth.Refresh(ctx, auth.Principal{CreatorID: j.creator})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || !p.CanJob(*j.ProfileID, j.creator, "downloads:write") {
			return errAuthority
		}
	} else if j.lease != "" {
		_, err := s.auth.Refresh(ctx, auth.Principal{Owner: true, CreatorID: "owner", SessionID: strings.TrimPrefix(j.lease, "session:")})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return profiles.ErrLocked
		}
	}
	p, err := s.profiles.Get(ctx, *j.ProfileID, j.lease)
	if errors.Is(err, profiles.ErrNotFound) {
		return errSourceChanged
	}
	if err != nil {
		return err
	}
	if p.Generation != j.generation {
		return errSourceChanged
	}
	if p.CredentialState == "locked" {
		return profiles.ErrLocked
	}
	return nil
}

func (s *Service) run(ctx context.Context, j job) {
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	watchDone := make(chan struct{})
	watchJob := j
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(s.limits.AuthInterval)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				if err := s.checkAuthority(watchCtx, watchJob); err != nil {
					s.op.Lock()
					if cancel := s.active[watchJob.ID]; cancel != nil {
						cancel(err)
					}
					s.op.Unlock()
					return
				}
			}
		}
	}()
	defer func() { stopWatch(); <-watchDone }()
	var err error
	for attempt := 0; attempt < s.limits.MaxAttempts; attempt++ {
		if err = s.checkAuthority(ctx, j); err != nil {
			break
		}
		j, err = s.load(ctx, j.ID)
		if err != nil {
			break
		}
		err = s.transfer(ctx, j)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			err = context.Cause(ctx)
			break
		}
		delay, again := retryDelay(err, s.limits.RetryBase, attempt)
		if !again || delay > s.limits.RequestTimeout || attempt+1 == s.limits.MaxAttempts {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			err = context.Cause(ctx)
		case <-timer.C:
		}
		if ctx.Err() != nil {
			break
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if authority := s.checkAuthority(context.Background(), j); authority != nil {
			err = authority
		} else if errors.Is(err, context.Canceled) && ctx.Err() == nil {
			// Profile invalidation cancels Access before its database change commits.
			err = errSourceChanged
		}
	}
	state, problem, restart := classify(err)
	encoded, _ := json.Marshal(problem)
	_, _ = s.db.ExecContext(context.Background(), `UPDATE download_jobs SET state=?,error=?,restart_required=CASE WHEN ? THEN 1 ELSE restart_required END,updated_at=? WHERE id=? AND state='running'`, state, string(encoded), restart, time.Now().UnixMilli(), j.ID)
	if current, e := s.load(context.Background(), j.ID); e == nil {
		s.publish(current)
	}
}

type temporaryError struct{ delay time.Duration }

func (e *temporaryError) Error() string { return "source temporarily unavailable" }

func retryDelay(err error, base time.Duration, attempt int) (time.Duration, bool) {
	var temporary *temporaryError
	var api *content.Error
	delay := base * time.Duration(1<<attempt)
	delay += time.Duration(rand.Int64N(max(int64(delay/4), 1)))
	if errors.As(err, &temporary) {
		return max(temporary.delay, delay), true
	}
	if errors.As(err, &api) {
		if api.Status == 503 || api.Status == 504 || (api.Status == 502 && api.Code == "upstream_unavailable") {
			return max(parseRetryAfter(api.Headers.Get("Retry-After")), delay), true
		}
		return 0, false
	}
	if !errors.Is(err, upstream.ErrNetwork) && !errors.Is(err, upstream.ErrTimeout) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, false
	}
	return delay, true
}

func classify(err error) (string, *JobError, bool) {
	problem := &JobError{Code: "upstream_unavailable", Message: "The download could not be completed."}
	state := "failed"
	restart := false
	var api *content.Error
	switch {
	case errors.Is(err, ErrRestart):
		state = "interrupted"
		restart = true
		problem.Code = "restart_required"
		problem.Message = "The file cannot be resumed safely. Restart it from zero."
	case errors.Is(err, profiles.ErrLocked):
		state = "waiting_for_credentials"
		problem.Code = "credentials_required"
		problem.Message = "Source credentials are required to continue."
	case errors.Is(err, errAuthority):
		state = "cancelled"
		problem.Code = "forbidden"
		problem.Message = "The download's authorization is no longer valid."
	case errors.Is(err, errSourceChanged):
		state = "cancelled"
		problem.Code = "unsafe_path"
		problem.Message = "The source profile changed or was removed."
	case errors.Is(err, ErrBudget), errors.Is(err, upstream.ErrResponseLimit):
		problem.Code = "storage_full"
		problem.Message = "The download storage limit or free-space reserve was reached."
	case errors.Is(err, ErrDestinationUnavailable):
		state = "interrupted"
		problem.Code = "unavailable"
		problem.Message = "The selected download folder is unavailable. Reconnect it, then resume."
	case errors.Is(err, downloadfs.ErrExists):
		problem.Code = "invalid_state"
		problem.Message = "The destination file already exists."
	case errors.Is(err, downloadfs.ErrUnsafe), errors.Is(err, downloadfs.ErrInvalid):
		problem.Code = "unsafe_path"
		problem.Message = "The local download path is not safe."
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		state = "interrupted"
		problem.Code = "unavailable"
		problem.Message = "The download was interrupted."
	case errors.As(err, &api):
		problem.Code = api.Code
		problem.Message = api.Message
		if api.Code == "upstream_auth_required" {
			state = "waiting_for_credentials"
		}
		if api.Code == "representation_changed" {
			state = "interrupted"
			restart = true
		}
		if api.Code == "budget_exceeded" {
			problem.Code = "storage_full"
		}
	default:
		var fileError *os.PathError
		if errors.As(err, &fileError) {
			problem.Code = "storage_full"
			problem.Message = "The destination could not accept the downloaded data."
		}
	}
	return state, problem, restart
}

func (s *Service) reconcile(ctx context.Context) error {
	cursor := ""
	for {
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM download_jobs WHERE id>? AND (state='running' OR publishing=1 OR file_available=1) ORDER BY id LIMIT 200`, cursor)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			j, err := s.load(ctx, id)
			if err != nil {
				return err
			}
			if j.publishing {
				dir, e := s.files.OpenJob(id, false)
				if e == nil {
					existing, openErr := dir.OpenCompleted(j.Name)
					if openErr == nil {
						existing.Close()
					} else if errors.Is(openErr, os.ErrNotExist) {
						part, partErr := dir.OpenPart()
						e = partErr
						if e == nil {
							var hash string
							hash, e = fileHash(ctx, part, j.TransferredBytes)
							part.Close()
							if e == nil && hash != j.checksum {
								e = ErrRestart
							}
						}
						if e == nil {
							e = dir.Publish(j.Name, j.TransferredBytes)
						}
					} else if errors.Is(openErr, downloadfs.ErrUnsafe) {
						// Publish validates the two-name hardlink crash window before unlinking.
						e = dir.Publish(j.Name, j.TransferredBytes)
					} else {
						e = openErr
					}
					if e == nil || errors.Is(e, downloadfs.ErrExists) {
						var f *os.File
						f, e = dir.OpenCompleted(j.Name)
						if e == nil {
							var hash string
							hash, e = fileHash(ctx, f, j.TransferredBytes)
							f.Close()
							if e == nil && hash != j.checksum {
								e = ErrRestart
							}
						}
					}
					dir.Close()
				}
				if e == nil {
					if j.DestinationID != "server" {
						// Leave the copy resumable without starting filesystem work or
						// overriding a pause/cancel decision during startup.
						_, err = s.db.ExecContext(ctx, `UPDATE download_jobs SET state=CASE WHEN state IN ('running','queued') THEN 'interrupted' ELSE state END,file_available=0,updated_at=? WHERE id=?`, time.Now().UnixMilli(), id)
					} else {
						_, err = s.db.ExecContext(ctx, `UPDATE download_jobs SET state='completed',file_available=1,publishing=0,error=NULL,updated_at=? WHERE id=?`, time.Now().UnixMilli(), id)
					}
				} else {
					_, err = s.db.ExecContext(ctx, `UPDATE download_jobs SET state='interrupted',file_available=0,restart_required=1,publishing=0,updated_at=? WHERE id=?`, time.Now().UnixMilli(), id)
				}
				if err != nil {
					return err
				}
				continue
			}
			if j.FileAvailable {
				if j.DestinationID != "server" {
					s.cleanupMountedStaging(j)
					if root, ok := s.mounts[j.DestinationID]; ok {
						if mount, openErr := downloadfs.OpenMount(root.Path); openErr == nil {
							_ = mount.Cleanup(j.DestinationPath, j.ID)
							mount.Close()
						}
					}
				} else {
					dir, e := s.files.OpenJob(id, false)
					if e == nil {
						f, openErr := dir.OpenCompleted(j.Name)
						e = openErr
						if e == nil {
							info, statErr := f.Stat()
							e = statErr
							if e == nil && info.Size() != j.TransferredBytes {
								e = ErrRestart
							}
							f.Close()
						}
						dir.Close()
					}
					if e != nil {
						if _, err = s.db.ExecContext(ctx, `UPDATE download_jobs SET file_available=0 WHERE id=?`, id); err != nil {
							return err
						}
					}
				}
			}
			if j.State == "running" {
				if _, err = s.db.ExecContext(ctx, `UPDATE download_jobs SET state='interrupted',restart_required=CASE WHEN transferred>0 AND (range_verified=0 OR total_bytes IS NULL OR etag='') THEN 1 ELSE restart_required END,updated_at=? WHERE id=?`, time.Now().UnixMilli(), id); err != nil {
					return err
				}
			}
		}
		cursor = ids[len(ids)-1]
	}
}

func fileHash(ctx context.Context, f *os.File, expected int64) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	buf := make([]byte, 64<<10)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, err := f.Read(buf)
		if count > 0 {
			n += int64(count)
			if n > expected {
				return "", ErrRestart
			}
			hash.Write(buf[:count])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if n != expected {
		return "", ErrRestart
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
