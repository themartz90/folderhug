package scans

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/upstream"
)

func (s *Service) worker() {
	defer s.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.ctx.Err() != nil {
			return
		}
		j, ctx, ok := s.claim()
		if ok {
			s.run(ctx, j)
			s.mu.Lock()
			if cancel := s.active[j.ID]; cancel != nil {
				cancel()
			}
			delete(s.active, j.ID)
			delete(s.origins, j.Origin)
			s.mu.Unlock()
			s.notify()
			continue
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

func (s *Service) claim() (job, context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query := "SELECT " + jobColumns + " FROM scan_jobs WHERE state='queued'"
	args := []any{}
	for origin := range s.origins {
		query += " AND origin<>?"
		args = append(args, origin)
	}
	rows, err := s.db.QueryContext(s.ctx, query+" ORDER BY created_at,id LIMIT 1", args...)
	if err != nil {
		return job{}, nil, false
	}
	var chosen job
	for rows.Next() {
		j, err := decodeJob(rows)
		if err != nil {
			rows.Close()
			return job{}, nil, false
		}
		if !s.origins[j.Origin] {
			chosen = j
			break
		}
	}
	rows.Close()
	if chosen.ID == "" {
		return job{}, nil, false
	}
	result, err := s.db.ExecContext(s.ctx, "UPDATE scan_jobs SET state='running',updated_at=? WHERE id=? AND state='queued'", stamp(time.Now()), chosen.ID)
	if err != nil {
		return job{}, nil, false
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return job{}, nil, false
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.active[chosen.ID] = cancel
	s.origins[chosen.Origin] = true
	chosen.State = "running"
	s.publish(chosen.Scan, chosen.CreatorID)
	return chosen, ctx, true
}

func (s *Service) run(ctx context.Context, initial job) {
	for {
		j, err := s.load(ctx, initial.ID)
		if err != nil {
			if ctx.Err() != nil {
				s.finish(initial, "interrupted", "", 0)
			}
			return
		}
		if j.State != "running" {
			return
		}
		if _, err = s.jobPrincipal(ctx, j); err != nil {
			s.finish(j, "cancelled", "forbidden", 0)
			return
		}
		if j.ElapsedSeconds >= float64(*j.Input.MaxSeconds) {
			s.finish(j, "partial", "budget_exceeded", 0)
			return
		}
		var escaped string
		var depth int
		err = s.db.QueryRowContext(ctx, "SELECT escaped_path,depth FROM scan_frontier WHERE scan_id=? AND state='pending' ORDER BY depth,path_key LIMIT 1", j.ID).Scan(&escaped, &depth)
		if errors.Is(err, sql.ErrNoRows) {
			s.complete(ctx, j)
			return
		}
		if err != nil {
			s.finish(j, "failed", "internal_error", 0)
			return
		}
		started := time.Now()
		stepCtx, cancel, err := s.reservePhase(ctx, j, 0)
		if err != nil {
			s.finish(j, "failed", "internal_error", time.Since(started).Seconds())
			return
		}
		stopWatch := s.watchToken(stepCtx, j, cancel)
		entries, err := s.directory(stepCtx, j, escaped)
		stopWatch()
		elapsed := time.Since(started).Seconds()
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				s.finish(j, "interrupted", "", elapsed)
				return
			}
			if _, refreshErr := s.jobPrincipal(context.Background(), j); refreshErr != nil {
				s.finish(j, "cancelled", "forbidden", elapsed)
				return
			}
			if errors.Is(err, profiles.ErrLocked) {
				s.finish(j, "waiting_for_credentials", "credentials_required", elapsed)
				return
			}
			if errors.Is(err, auth.ErrUnauthenticated) {
				s.finish(j, "cancelled", "forbidden", elapsed)
				return
			}
			current, getErr := s.profiles.Get(context.Background(), j.ProfileID, j.LeaseID)
			if getErr != nil || current.Generation != j.Generation {
				s.finish(j, "failed", "invalid_state", elapsed)
				return
			}
			if current.CredentialState == "locked" {
				s.finish(j, "waiting_for_credentials", "credentials_required", elapsed)
				return
			}
			if j.ElapsedSeconds+elapsed >= float64(*j.Input.MaxSeconds) {
				s.finish(j, "partial", "budget_exceeded", elapsed)
				return
			}
			if err = s.failedDirectory(ctx, j, escaped, elapsed); err != nil {
				elapsed = time.Since(started).Seconds()
				state, code := "failed", "internal_error"
				if j.ElapsedSeconds+elapsed >= float64(*j.Input.MaxSeconds) {
					state, code = "partial", "budget_exceeded"
				}
				s.finish(j, state, code, elapsed)
				return
			}
			continue
		}
		if j.EntriesSeen+len(entries) > *j.Input.MaxEntries {
			cancel()
			s.finish(j, "partial", "budget_exceeded", elapsed)
			return
		}
		if _, err = s.jobPrincipal(ctx, j); err != nil {
			cancel()
			s.finish(j, "cancelled", "forbidden", elapsed)
			return
		}
		cancel()
		stepCtx, cancel, err = s.reservePhase(ctx, j, time.Since(started))
		if err != nil {
			state, code := "failed", "internal_error"
			if j.ElapsedSeconds+time.Since(started).Seconds() >= float64(*j.Input.MaxSeconds) {
				state, code = "partial", "budget_exceeded"
			}
			s.finish(j, state, code, time.Since(started).Seconds())
			return
		}
		err = s.persistDirectory(stepCtx, j, escaped, depth, entries, started)
		cancel()
		if err != nil {
			elapsed = time.Since(started).Seconds()
			if ctx.Err() != nil {
				s.finish(j, "interrupted", "", elapsed)
			} else if j.ElapsedSeconds+elapsed >= float64(*j.Input.MaxSeconds) {
				s.finish(j, "partial", "budget_exceeded", elapsed)
			} else {
				s.finish(j, "failed", "internal_error", elapsed)
			}
			return
		}
		if updated, err := s.load(ctx, j.ID); err == nil {
			s.publish(updated.Scan, j.CreatorID)
		}
	}
}

func (s *Service) watchToken(ctx context.Context, j job, cancel context.CancelFunc) func() {
	if j.Owner {
		return func() {}
	}
	watchCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTicker(250 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-timer.C:
				if _, err := s.jobPrincipal(watchCtx, j); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return func() { stop(); <-done }
}

func (s *Service) directory(ctx context.Context, j job, escaped string) ([]listing.Entry, error) {
	p, credentials, active, cancel, err := s.profiles.Access(ctx, j.ProfileID, j.LeaseID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if p.Generation != j.Generation {
		return nil, ErrInvalidState
	}
	root, err := upstream.NewRoot(p.RootURL, p.AllowHTTP)
	if err != nil {
		return nil, err
	}
	response, err := s.fetcher.Do(active, root, http.MethodGet, escaped, upstream.Options{Credentials: credentials, MaxBytes: listing.MaxBytes, Timeout: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return nil, profiles.ErrLocked
	}
	if response.StatusCode != 200 {
		return nil, upstream.ErrNetwork
	}
	current, err := root.Resolve(escaped)
	if err != nil {
		return nil, err
	}
	if response.Request != nil && response.Request.URL != nil {
		if strings.TrimSuffix(response.Request.URL.Path, "/") != strings.TrimSuffix(current.Path, "/") {
			return nil, listing.ErrUnsupported
		}
		current = response.Request.URL
	}
	entries, _, err := listing.Parse(response.Body, root, current, j.ProfileID, j.Generation)
	if err != nil {
		return nil, err
	}
	if err = active.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *Service) persistDirectory(ctx context.Context, j job, escaped string, depth int, entries []listing.Entry, started time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	var generation int
	if err = tx.QueryRowContext(ctx, "SELECT s.state,p.generation FROM scan_jobs s JOIN profiles p ON p.id=s.profile_id WHERE s.id=?", j.ID).Scan(&state, &generation); err != nil {
		return err
	}
	if state != "running" || generation != j.Generation {
		return ErrInvalidState
	}
	catalogSQL := `INSERT INTO catalog_entries(profile_id,generation,id,parent_id,escaped_path,metadata) VALUES(?,?,?,?,?,?) ON CONFLICT(profile_id,generation,id) DO UPDATE SET parent_id=excluded.parent_id,escaped_path=excluded.escaped_path,metadata=excluded.metadata`
	indexSQL := `INSERT INTO scan_index(profile_id,generation,entry_id,escaped_path,path_key,name_fold,path_fold,natural_key,category,extension,size,modified_at,metadata,last_scan_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(profile_id,generation,entry_id) DO UPDATE SET escaped_path=excluded.escaped_path,path_key=excluded.path_key,name_fold=excluded.name_fold,path_fold=excluded.path_fold,natural_key=excluded.natural_key,category=excluded.category,extension=excluded.extension,size=excluded.size,modified_at=excluded.modified_at,metadata=excluded.metadata,last_scan_id=excluded.last_scan_id`
	catalogStmt, err := tx.PrepareContext(ctx, catalogSQL)
	if err != nil {
		return err
	}
	defer catalogStmt.Close()
	indexStmt, err := tx.PrepareContext(ctx, indexSQL)
	if err != nil {
		return err
	}
	defer indexStmt.Close()
	resultStmt, err := tx.PrepareContext(ctx, "INSERT INTO scan_results(scan_id,entry_id,metadata) VALUES(?,?,?) ON CONFLICT(scan_id,entry_id) DO UPDATE SET metadata=excluded.metadata")
	if err != nil {
		return err
	}
	defer resultStmt.Close()
	frontierStmt, err := tx.PrepareContext(ctx, "INSERT OR IGNORE INTO scan_frontier(scan_id,path_key,escaped_path,depth) VALUES(?,?,?,?)")
	if err != nil {
		return err
	}
	defer frontierStmt.Close()
	files, skipped := 0, 0
	for _, entry := range entries {
		encoded, _ := json.Marshal(entry)
		if _, err = catalogStmt.ExecContext(ctx, j.ProfileID, j.Generation, entry.ID, entry.ParentID, entry.EscapedPath, string(encoded)); err != nil {
			return err
		}
		if entry.Kind == "directory" {
			if depth >= *j.Input.MaxDepth || isExcluded(j.Input, j.RootPath, entry.EscapedPath) {
				skipped++
			} else {
				pathKey, _ := url.PathUnescape(entry.EscapedPath)
				if _, err = frontierStmt.ExecContext(ctx, j.ID, pathKey, entry.EscapedPath, depth+1); err != nil {
					return err
				}
			}
		} else {
			files++
		}
		if !selectedCategory(j.Input, entry.Category) {
			continue
		}
		decoded, _ := url.PathUnescape(entry.EscapedPath)
		var modified any
		if entry.TimezoneKnown && entry.ModifiedAt != nil {
			modified = stamp(*entry.ModifiedAt)
		}
		if _, err = indexStmt.ExecContext(ctx, j.ProfileID, j.Generation, entry.ID, entry.EscapedPath, decoded, strings.ToLower(entry.Name), strings.ToLower(decoded), naturalKey(entry.Name), entry.Category, entry.Extension, entry.Size, modified, string(encoded), j.ID); err != nil {
			return err
		}
		if _, err = resultStmt.ExecContext(ctx, j.ID, entry.ID, string(encoded)); err != nil {
			return err
		}
	}
	pathKey, _ := url.PathUnescape(escaped)
	if _, err = tx.ExecContext(ctx, "UPDATE scan_frontier SET state='done' WHERE scan_id=? AND path_key=?", j.ID, pathKey); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE scan_jobs SET directories_visited=directories_visited+1,files_found=files_found+?,directories_skipped=directories_skipped+?,entries_seen=entries_seen+?,partial=(partial OR ?>0),updated_at=? WHERE id=?", files, skipped, len(entries), skipped, stamp(time.Now()), j.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE scan_index_revision SET revision=revision+1 WHERE id=1"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.settlePhase(ctx, j, started)
}

func (s *Service) failedDirectory(ctx context.Context, j job, escaped string, elapsed float64) error {
	consumed := time.Duration(elapsed * float64(time.Second))
	started := time.Now().Add(-consumed)
	ctx, cancel, err := s.reservePhase(ctx, j, consumed)
	if err != nil {
		return err
	}
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	pathKey, _ := url.PathUnescape(escaped)
	if _, err = tx.ExecContext(ctx, "UPDATE scan_frontier SET state='failed',error_code='upstream_unavailable' WHERE scan_id=? AND path_key=?", j.ID, pathKey); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE scan_jobs SET directories_failed=directories_failed+1,partial=1,updated_at=? WHERE id=? AND state='running'", stamp(time.Now()), j.ID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.settlePhase(ctx, j, started)
}

func (s *Service) finish(j job, state, code string, elapsed float64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var encoded any
	if code != "" {
		body, _ := json.Marshal(ApiError{Code: code, Message: safeMessage(code), RequestID: j.ID})
		encoded = string(body)
	}
	result, err := s.db.ExecContext(ctx, "UPDATE scan_jobs SET state=CASE WHEN state='running' THEN ? ELSE state END,elapsed_seconds=CASE WHEN ?>0 THEN ? ELSE elapsed_seconds END,active_since=NULL,partial=(partial OR ?),updated_at=?,error=CASE WHEN state='running' THEN ? ELSE error END WHERE id=? AND state IN ('running','paused','cancelled')", state, elapsed, j.ElapsedSeconds+elapsed, state != "completed", stamp(time.Now()), encoded, j.ID)
	if err != nil {
		return
	}
	n, _ := result.RowsAffected()
	if n > 0 {
		if current, err := s.load(ctx, j.ID); err == nil {
			s.publish(current.Scan, j.CreatorID)
		}
	}
}
func safeMessage(code string) string {
	switch code {
	case "credentials_required":
		return "Unlock this profile to continue the scan."
	case "budget_exceeded":
		return "The scan reached its configured limit."
	case "forbidden":
		return "The scan's API token no longer permits this work."
	case "invalid_state":
		return "The source configuration changed."
	case "scan_superseded":
		return "A newer scan overlaps this folder. Start a new scan."
	default:
		return "The scan could not continue."
	}
}

func (s *Service) complete(ctx context.Context, j job) {
	if j.Partial || j.DirectoriesFailed > 0 || j.DirectoriesSkipped > 0 {
		s.finish(j, "partial", "", 0)
		return
	}
	started := time.Now()
	ctx, cancel, err := s.reservePhase(ctx, j, 0)
	if err != nil {
		s.finish(j, "failed", "internal_error", time.Since(started).Seconds())
		return
	}
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.finish(j, "failed", "internal_error", time.Since(started).Seconds())
		return
	}
	committed := false
	defer func() {
		tx.Rollback()
		if !committed {
			state, code := "failed", "internal_error"
			if j.ElapsedSeconds+time.Since(started).Seconds() >= float64(*j.Input.MaxSeconds) {
				state, code = "partial", "budget_exceeded"
			}
			s.finish(j, state, code, time.Since(started).Seconds())
		}
	}()
	rootKey, _ := url.PathUnescape(j.RootPath)
	query := "DELETE FROM scan_index WHERE profile_id=? AND generation=? AND substr(path_key,1,length(?))=? AND path_key<>? AND last_scan_id<>?"
	args := []any{j.ProfileID, j.Generation, rootKey, rootKey, rootKey, j.ID}
	if len(j.Input.Categories) > 0 {
		query += " AND category IN (" + strings.TrimSuffix(strings.Repeat("?,", len(j.Input.Categories)), ",") + ")"
		for _, category := range j.Input.Categories {
			args = append(args, category)
		}
	}
	var state string
	var generation int
	if err = tx.QueryRowContext(ctx, "SELECT s.state,p.generation FROM scan_jobs s JOIN profiles p ON p.id=s.profile_id WHERE s.id=?", j.ID).Scan(&state, &generation); err != nil || state != "running" || generation != j.Generation {
		return
	}
	if _, err = tx.ExecContext(ctx, query, args...); err != nil {
		return
	}
	if _, err = tx.ExecContext(ctx, "UPDATE scan_index_revision SET revision=revision+1 WHERE id=1"); err != nil {
		return
	}
	if _, err = tx.ExecContext(ctx, "UPDATE scan_jobs SET state='completed',active_since=NULL,updated_at=? WHERE id=? AND state='running'", stamp(time.Now()), j.ID); err != nil {
		return
	}
	if err = tx.Commit(); err != nil {
		return
	}
	committed = true
	_ = s.settlePhase(ctx, j, started)
	j.State = "completed"
	s.publish(j.Scan, j.CreatorID)
}
