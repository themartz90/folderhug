package downloads

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/themartz90/folderhug/internal/content"
	"github.com/themartz90/folderhug/internal/downloadfs"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/upstream"
)

func strongETag(tag string) bool {
	if len(tag) < 2 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		return false
	}
	for _, r := range tag[1 : len(tag)-1] {
		if r < 0x21 || r == '"' || r == 0x7f {
			return false
		}
	}
	return true
}

func (s *Service) transfer(ctx context.Context, j job) error {
	if j.publishing && j.DestinationID != "server" {
		internal, err := s.files.OpenJob(j.ID, false)
		if err == nil {
			f, e := internal.OpenCompleted(j.Name)
			if e == nil {
				f.Close()
				internal.Close()
				return s.publishMounted(ctx, j)
			}
			internal.Close()
		}
	}
	if j.ProfileID == nil {
		return errSourceChanged
	}
	p, credentials, active, cancel, err := s.profiles.Access(ctx, *j.ProfileID, j.lease)
	if err != nil {
		return err
	}
	defer cancel()
	if p.Generation != j.generation {
		return errSourceChanged
	}
	root, err := upstream.NewRoot(p.RootURL, p.AllowHTTP)
	if err != nil {
		return err
	}
	if _, err = root.Resolve(j.path); err != nil {
		return err
	}
	source := content.Source{Root: root, Path: j.path, Credentials: credentials}
	if j.TransferredBytes > 0 {
		source.ExpectedETag = j.etag
	}
	meta, err := content.New(s.fetcher, content.Limits{MaxBytes: s.limits.StorageBudgetBytes, Timeout: s.limits.RequestTimeout}).Metadata(active, source)
	if err != nil {
		return err
	}
	if j.TransferredBytes > 0 && (j.restart || !j.ranges || !strongETag(j.etag) || j.TotalBytes == nil || meta.Size == nil || *meta.Size != *j.TotalBytes || meta.ETag != j.etag) {
		return ErrRestart
	}
	if j.TransferredBytes == 0 {
		j.TotalBytes = meta.Size
		j.etag = meta.ETag
		j.ranges = false
		j.Connections = 1
		j.FallbackReason = nil
		if j.TotalBytes != nil && *j.TotalBytes > s.limits.StorageBudgetBytes {
			return ErrBudget
		}
		if meta.Size != nil && *meta.Size > 0 && meta.StrongETag {
			j.ranges, err = s.probe(active, root, credentials, j)
			if err != nil {
				return err
			}
		}
		if j.requested > 1 {
			if j.ranges && j.TotalBytes != nil && *j.TotalBytes >= int64(j.requested) {
				j.Connections = j.requested
			} else {
				reason := "The source cannot provide verified parallel ranges."
				j.FallbackReason = &reason
			}
		}
	}
	dir, err := s.files.OpenJob(j.ID, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	f, err := dir.OpenPart()
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			f.Close()
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if j.TransferredBytes == 0 {
		if err = f.Truncate(0); err != nil {
			return err
		}
		if err = s.initialize(active, j); err != nil {
			return err
		}
		info, err = f.Stat()
		if err != nil {
			return err
		}
	} else if j.TotalBytes != nil && info.Size() > *j.TotalBytes {
		return ErrRestart
	}
	segments, err := s.segments(active, j.ID)
	if err != nil {
		return err
	}
	if err = validateSegments(j, segments, info.Size()); err != nil {
		return err
	}
	limit := s.limits.UnknownSizeMaxBytes
	if j.TotalBytes != nil {
		limit = *j.TotalBytes
	}
	if err = s.reserve(j.ID, limit, info.Size(), j.TransferredBytes); err != nil {
		return err
	}
	defer s.release(j.ID)
	transferCtx, stop := context.WithCancelCause(active)
	defer stop(nil)
	speed := &throttle{bps: j.bps}
	var wg sync.WaitGroup
	failures := make(chan error, len(segments))
	for _, part := range segments {
		wg.Add(1)
		go func(part segment) {
			defer wg.Done()
			if err := s.copySegment(transferCtx, root, credentials, j, part, f, speed); err != nil {
				failures <- err
				stop(err)
			}
		}(part)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if !errors.Is(err, context.Canceled) {
			return err
		}
	}
	if err := transferCtx.Err(); err != nil {
		return context.Cause(transferCtx)
	}
	if err = s.checkAuthority(active, j); err != nil {
		return err
	}
	j, err = s.load(active, j.ID)
	if err != nil {
		return err
	}
	if j.State != "running" {
		return context.Canceled
	}
	if j.TotalBytes != nil && j.TransferredBytes != *j.TotalBytes {
		return ErrRestart
	}
	if err = f.Sync(); err != nil {
		return err
	}
	hash, err := fileHash(active, f, j.TransferredBytes)
	if err != nil {
		return err
	}
	closed = true
	if err = f.Close(); err != nil {
		return err
	}
	if err := s.publishInternal(active, j, hash, dir); err != nil {
		return err
	}
	if j.DestinationID != "server" {
		j.checksum = hash
		return s.publishMounted(active, j)
	}
	return nil
}

func (s *Service) publishInternal(active context.Context, j job, hash string, dir *downloadfs.Job) error {
	// A pause or cancel cannot race the final publication state change.
	s.op.Lock()
	defer s.op.Unlock()
	current, err := s.load(active, j.ID)
	if err != nil {
		return err
	}
	if current.State != "running" {
		return context.Canceled
	}
	if err = s.checkAuthority(active, current); err != nil {
		return err
	}
	if existing, e := dir.OpenCompleted(j.Name); e == nil {
		existing.Close()
		return downloadfs.ErrExists
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	result, err := s.db.ExecContext(active, `UPDATE download_jobs SET publishing=1,checksum=?,updated_at=? WHERE id=? AND state='running'`, hash, time.Now().UnixMilli(), j.ID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return context.Canceled
	}
	if err = active.Err(); err != nil {
		return err
	}
	s.budgetMu.Lock()
	err = dir.Publish(j.Name, j.TransferredBytes)
	s.budgetMu.Unlock()
	if err != nil {
		if errors.Is(err, downloadfs.ErrExists) || errors.Is(err, downloadfs.ErrUnsafe) || errors.Is(err, downloadfs.ErrSize) {
			_, _ = s.db.ExecContext(context.Background(), `UPDATE download_jobs SET publishing=0 WHERE id=?`, j.ID)
		}
		return err
	}
	if j.DestinationID != "server" {
		return nil
	}
	_, err = s.db.ExecContext(context.Background(), `UPDATE download_jobs SET state='completed',publishing=0,file_available=1,error=NULL,updated_at=? WHERE id=?`, time.Now().UnixMilli(), j.ID)
	if err == nil {
		if completed, e := s.load(context.Background(), j.ID); e == nil {
			s.publish(completed)
		}
	}
	return err
}

func validateSegments(j job, parts []segment, physical int64) error {
	if len(parts) != j.Connections || (j.Connections != 1 && j.Connections != 2 && j.Connections != 4) || j.TransferredBytes < 0 {
		return ErrRestart
	}
	var durable int64
	for i, part := range parts {
		start, end := int64(0), int64(-1)
		if j.TotalBytes != nil {
			size := *j.TotalBytes
			if size < 0 || (j.Connections > 1 && size < int64(j.Connections)) {
				return ErrRestart
			}
			start = (size/int64(j.Connections))*int64(i) + min(int64(i), size%int64(j.Connections))
			length := size / int64(j.Connections)
			if int64(i) < size%int64(j.Connections) {
				length++
			}
			end = start + length - 1
		}
		if part.number != i || part.start != start || part.end != end || part.done < 0 || (j.TotalBytes != nil && part.done > end-start+1) || (part.done > 0 && (part.done > physical || start > physical-part.done)) || part.done > j.TransferredBytes-durable {
			return ErrRestart
		}
		durable += part.done
	}
	if durable != j.TransferredBytes {
		return ErrRestart
	}
	return nil
}

func (s *Service) initialize(ctx context.Context, j job) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM download_segments WHERE job_id=?`, j.ID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE download_jobs SET total_bytes=?,etag=?,range_verified=?,connections=?,fallback_reason=?,restart_required=0,publishing=0,checksum='' WHERE id=?`, j.TotalBytes, j.etag, j.ranges, j.Connections, j.FallbackReason, j.ID)
	if err != nil {
		return err
	}
	for i := 0; i < j.Connections; i++ {
		start, end := int64(0), int64(-1)
		if j.TotalBytes != nil && *j.TotalBytes > 0 {
			size := *j.TotalBytes
			start = (size/int64(j.Connections))*int64(i) + min(int64(i), size%int64(j.Connections))
			length := size / int64(j.Connections)
			if int64(i) < size%int64(j.Connections) {
				length++
			}
			end = start + length - 1
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO download_segments(job_id,number,start_byte,end_byte) VALUES(?,?,?,?)`, j.ID, i, start, end); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) segments(ctx context.Context, id string) ([]segment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT number,start_byte,end_byte,durable_bytes FROM download_segments WHERE job_id=? ORDER BY number`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []segment{}
	for rows.Next() {
		var seg segment
		if err = rows.Scan(&seg.number, &seg.start, &seg.end, &seg.done); err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 || len(out) > 4 {
		return nil, ErrRestart
	}
	return out, nil
}

func (s *Service) probe(ctx context.Context, root *upstream.Root, credentials *upstream.Credentials, j job) (bool, error) {
	res, err := s.fetcher.Do(ctx, root, "GET", j.path, upstream.Options{Credentials: credentials, Headers: http.Header{"Range": {"bytes=0-0"}, "If-Match": {j.etag}, "If-Range": {j.etag}}, MaxBytes: s.limits.StorageBudgetBytes, Timeout: 30 * time.Second})
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode == 200 {
		return false, nil
	}
	if res.StatusCode != 206 {
		return false, statusError(res)
	}
	if !identity(res) || res.Header.Get("ETag") != j.etag {
		return false, ErrRestart
	}
	if !validRange(res, 0, 0, *j.TotalBytes) {
		return false, nil
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 2))
	if err != nil {
		return false, err
	}
	if len(b) != 1 {
		return false, nil
	}
	return true, nil
}

func identity(res *http.Response) bool {
	return res.Header.Get("Content-Encoding") == "" || strings.EqualFold(res.Header.Get("Content-Encoding"), "identity")
}
func validRange(res *http.Response, start, end, total int64) bool {
	if res.StatusCode != 206 || res.ContentLength != end-start+1 || res.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", start, end, total) {
		return false
	}
	if raw := res.Header.Get("Content-Length"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n != res.ContentLength {
			return false
		}
	}
	return true
}

func statusError(res *http.Response) error {
	switch res.StatusCode {
	case 401, 403:
		return profiles.ErrLocked
	case 412:
		return ErrRestart
	case 429, 503:
		return &temporaryError{parseRetryAfter(res.Header.Get("Retry-After"))}
	}
	if res.StatusCode >= 500 {
		return &temporaryError{}
	}
	return &JobError{Code: "upstream_unavailable", Message: "The source could not provide the requested file."}
}
func parseRetryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(0, time.Until(at))
	}
	return 0
}

func (s *Service) copySegment(ctx context.Context, root *upstream.Root, credentials *upstream.Credentials, j job, part segment, f *os.File, speed *throttle) error {
	if part.end >= 0 && part.done == part.end-part.start+1 {
		return nil
	}
	if err := s.checkAuthority(ctx, j); err != nil {
		return err
	}
	headers := make(http.Header)
	ranged := j.Connections > 1 || part.done > 0
	if strongETag(j.etag) {
		headers.Set("If-Match", j.etag)
	}
	start := part.start + part.done
	if ranged {
		if j.TotalBytes == nil || !j.ranges || !strongETag(j.etag) {
			return ErrRestart
		}
		headers.Set("Range", fmt.Sprintf("bytes=%d-%d", start, part.end))
		headers.Set("If-Range", j.etag)
	}
	limit := s.limits.UnknownSizeMaxBytes
	if j.TotalBytes != nil {
		limit = *j.TotalBytes
	}
	res, err := s.fetcher.Do(ctx, root, "GET", j.path, upstream.Options{Credentials: credentials, Headers: headers, MaxBytes: max(limit, 1), Timeout: s.limits.RequestTimeout})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	stop := context.AfterFunc(ctx, func() { res.Body.Close() })
	defer stop()
	if res.StatusCode != 200 && res.StatusCode != 206 {
		return statusError(res)
	}
	if !identity(res) || (strongETag(j.etag) && res.Header.Get("ETag") != j.etag) {
		return ErrRestart
	}
	if ranged {
		if !validRange(res, start, part.end, *j.TotalBytes) {
			return ErrRestart
		}
	} else {
		if res.StatusCode != 200 {
			return ErrRestart
		}
		if j.TotalBytes != nil && res.ContentLength != *j.TotalBytes {
			return ErrRestart
		}
		if res.ContentLength > limit {
			return ErrBudget
		}
		if raw := res.Header.Get("Content-Length"); raw != "" {
			n, e := strconv.ParseInt(raw, 10, 64)
			if e != nil || n != res.ContentLength {
				return ErrRestart
			}
		}
		if j.TotalBytes == nil && res.ContentLength >= 0 {
			size := res.ContentLength
			j.TotalBytes = &size
			part.end = size - 1
			_, err = s.db.ExecContext(ctx, `UPDATE download_jobs SET total_bytes=? WHERE id=?`, size, j.ID)
			if err != nil {
				return err
			}
			_, err = s.db.ExecContext(ctx, `UPDATE download_segments SET end_byte=? WHERE job_id=? AND number=?`, part.end, j.ID, part.number)
			if err != nil {
				return err
			}
		}
	}
	var expected int64 = -1
	if res.ContentLength >= 0 {
		expected = res.ContentLength
	}
	buffer := make([]byte, 64<<10)
	written := int64(0)
	checkpointBytes := int64(0)
	last := time.Now()
	checkpoint := func() error {
		if err := f.Sync(); err != nil {
			return err
		}
		// Cancellation must not discard an already synced checkpoint.
		tx, err := s.db.BeginTx(context.Background(), nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		_, err = tx.Exec(`UPDATE download_segments SET durable_bytes=? WHERE job_id=? AND number=?`, part.done+written, j.ID, part.number)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE download_jobs SET transferred=(SELECT COALESCE(sum(durable_bytes),0) FROM download_segments WHERE job_id=?),updated_at=? WHERE id=?`, j.ID, time.Now().UnixMilli(), j.ID)
		if err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		checkpointBytes = written
		last = time.Now()
		if update, e := s.load(context.Background(), j.ID); e == nil {
			s.publish(update)
		}
		return nil
	}
	for {
		if err = ctx.Err(); err != nil {
			if written > checkpointBytes {
				if syncErr := checkpoint(); syncErr != nil {
					return syncErr
				}
			}
			return err
		}
		n, readErr := res.Body.Read(buffer)
		if n > 0 {
			if int64(n) > limit-start-written {
				return ErrBudget
			}
			if expected >= 0 && int64(n) > expected-written {
				return ErrRestart
			}
			if err = speed.wait(ctx, n); err != nil {
				return err
			}
			if err = s.writePart(j.ID, f, buffer[:n], start+written); err != nil {
				return err
			}
			written += int64(n)
			if written-checkpointBytes >= 1<<20 || time.Since(last) >= s.limits.CheckpointInterval {
				if err = checkpoint(); err != nil {
					return err
				}
				if err = s.checkAuthority(ctx, j); err != nil {
					return err
				}
			}
		}
		if readErr != nil {
			if written > checkpointBytes {
				if err = checkpoint(); err != nil {
					return err
				}
			}
			if readErr != io.EOF {
				return readErr
			}
			if expected >= 0 && written != expected {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
}
