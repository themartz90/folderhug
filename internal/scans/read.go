package scans

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
)

type pageCursor struct {
	Kind, Binding, Key, ProfileID, ID string
	Revision                          int64
	Expires                           int64
}

func (s *Service) sign(c pageCursor) string {
	c.Expires = time.Now().Add(5 * time.Minute).Unix()
	b, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Service) verify(raw, kind, binding string) (pageCursor, error) {
	var c pageCursor
	if len(raw) > 4096 {
		return c, ErrSnapshotExpired
	}
	a, b, ok := strings.Cut(raw, ".")
	if !ok {
		return c, ErrSnapshotExpired
	}
	body, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil {
		return c, ErrSnapshotExpired
	}
	signature, err := base64.RawURLEncoding.DecodeString(b)
	if err != nil {
		return c, ErrSnapshotExpired
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), signature) || json.Unmarshal(body, &c) != nil || c.Kind != kind || c.Binding != binding || c.Expires <= time.Now().Unix() {
		return c, ErrSnapshotExpired
	}
	return c, nil
}
func fingerprint(value any) string {
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Service) visibleProfiles(ctx context.Context, p auth.Principal, requested string) ([]profiles.Profile, error) {
	if requested != "" {
		if !p.Can(requested, "browse:read") {
			return nil, ErrNotFound
		}
		profile, err := s.profiles.Get(ctx, requested, profiles.LeaseID(p))
		if err != nil {
			return nil, err
		}
		return []profiles.Profile{profile}, nil
	}
	query := "SELECT id FROM profiles"
	args := []any{}
	if !p.Owner {
		if len(p.ProfileIDs) > 256 {
			return nil, ErrBudget
		}
		if len(p.ProfileIDs) == 0 {
			return []profiles.Profile{}, nil
		}
		query += " WHERE id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(p.ProfileIDs)), ",") + ")"
		for _, id := range p.ProfileIDs {
			args = append(args, id)
		}
	}
	rows, err := s.db.QueryContext(ctx, query+" ORDER BY id LIMIT 257", args...)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		if p.Can(id, "browse:read") {
			ids = append(ids, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) > 256 {
		return nil, ErrBudget
	}
	visible := make([]profiles.Profile, 0, len(ids))
	for _, id := range ids {
		profile, err := s.profiles.Get(ctx, id, profiles.LeaseID(p))
		if err != nil {
			return nil, err
		}
		visible = append(visible, profile)
	}
	return visible, nil
}
func profileWhere(visible []profiles.Profile, prefix string) (string, []any) {
	if len(visible) == 0 {
		return "0", nil
	}
	parts := []string{}
	args := []any{}
	for _, p := range visible {
		parts = append(parts, "("+prefix+"profile_id=? AND "+prefix+"generation=?)")
		args = append(args, p.ID, p.Generation)
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func (s *Service) List(ctx context.Context, p auth.Principal, profileID string, limit int, cursor string) (ScanPage, error) {
	p, err := s.auth.Refresh(ctx, p)
	if err != nil {
		return ScanPage{}, err
	}
	if limit == 0 {
		limit = 200
	}
	if limit < 1 || limit > 1000 {
		return ScanPage{}, ErrInvalid
	}
	visible, err := s.visibleProfiles(ctx, p, profileID)
	if err != nil {
		return ScanPage{}, err
	}
	where, args := profileWhere(visible, "")
	if len(visible) > 0 {
		clauses := []string{}
		args = []any{}
		for _, profile := range visible {
			clauses = append(clauses, "profile_id=?")
			args = append(args, profile.ID)
		}
		where = "(" + strings.Join(clauses, " OR ") + ")"
	}
	binding := fingerprint([]any{p.CreatorID, profiles.LeaseID(p), visible, profileID, limit})
	if cursor != "" {
		c, err := s.verify(cursor, "scans", binding)
		if err != nil {
			return ScanPage{}, err
		}
		where += " AND (created_at<? OR (created_at=? AND id<?))"
		args = append(args, c.Key, c.Key, c.ID)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, "SELECT "+jobColumns+" FROM scan_jobs WHERE "+where+" ORDER BY created_at DESC,id DESC LIMIT ?", args...)
	if err != nil {
		return ScanPage{}, err
	}
	result := ScanPage{Scans: []Scan{}}
	for rows.Next() {
		j, err := decodeJob(rows)
		if err != nil {
			rows.Close()
			return result, err
		}
		result.Scans = append(result.Scans, j.Scan)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(result.Scans) > limit {
		result.Scans = result.Scans[:limit]
		last := result.Scans[limit-1]
		next := s.sign(pageCursor{Kind: "scans", Binding: binding, Key: stamp(last.CreatedAt), ID: last.ID})
		result.NextCursor = &next
	}
	return result, nil
}

func normalizeSearch(q SearchQuery) (SearchQuery, error) {
	if q.Limit == 0 {
		q.Limit = 200
	}
	if q.Match == "" {
		q.Match = "substring"
	}
	if len(q.Q) > 512 || !utf8.ValidString(q.Q) || q.Limit < 1 || q.Limit > 1000 || !slices.Contains([]string{"substring", "prefix"}, q.Match) || q.DirectoryID != "" && q.ProfileID == "" || len(q.Extensions) > 512 {
		return q, ErrInvalid
	}
	if q.Sort != "" && q.Sort != "name" || q.Direction != "" && q.Direction != "asc" || q.Refresh {
		return q, ErrInvalid
	}
	if q.MinSize != nil && *q.MinSize < 0 || q.MaxSize != nil && *q.MaxSize < 0 || q.MinSize != nil && q.MaxSize != nil && *q.MinSize > *q.MaxSize {
		return q, ErrInvalid
	}
	if q.ModifiedAfter != nil && q.ModifiedBefore != nil && q.ModifiedAfter.After(*q.ModifiedBefore) {
		return q, ErrInvalid
	}
	if q.Category != "" && !slices.Contains([]string{"folder", "video", "image", "audio", "document", "archive", "other"}, q.Category) {
		return q, ErrInvalid
	}
	if q.Category != "" && len(q.Categories) > 0 || len(q.Categories) > 7 {
		return q, ErrInvalid
	}
	for _, category := range q.Categories {
		if category == "" || category == "folder" || !slices.Contains([]string{"video", "image", "audio", "document", "archive", "other"}, category) {
			return q, ErrInvalid
		}
	}
	q.Categories = slices.Clone(q.Categories)
	slices.Sort(q.Categories)
	q.Categories = slices.Compact(q.Categories)
	q.Q = strings.ToLower(q.Q)
	if q.Extensions != "" {
		values := strings.Split(strings.ToLower(q.Extensions), ",")
		for i, v := range values {
			v = strings.TrimSpace(v)
			if v == "" || strings.ContainsAny(v, "./\\ \t\r\n") {
				return q, ErrInvalid
			}
			values[i] = v
		}
		slices.Sort(values)
		q.Extensions = strings.Join(slices.Compact(values), ",")
	}
	return q, nil
}

func (s *Service) Search(ctx context.Context, p auth.Principal, q SearchQuery) (SearchPage, error) {
	p, err := s.auth.Refresh(ctx, p)
	if err != nil {
		return SearchPage{}, err
	}
	q, err = normalizeSearch(q)
	if err != nil {
		return SearchPage{}, err
	}
	visible, err := s.visibleProfiles(ctx, p, q.ProfileID)
	if err != nil {
		return SearchPage{}, err
	}
	where, args := profileWhere(visible, "i.")
	rootPath := ""
	if q.DirectoryID != "" && q.DirectoryID != "root" {
		entry, err := s.catalog.Entry(ctx, q.ProfileID, profiles.LeaseID(p), q.DirectoryID)
		if err != nil {
			return SearchPage{}, err
		}
		if entry.Kind != "directory" {
			return SearchPage{}, ErrInvalid
		}
		rootPath, _ = url.PathUnescape(entry.EscapedPath)
		where += " AND substr(i.path_key,1,length(?))=?"
		args = append(args, rootPath, rootPath)
	}
	if q.DirectOnly {
		where += " AND instr(substr(i.path_key,length(?)+1),'/')=0"
		args = append(args, rootPath)
	}
	if q.Q != "" && q.Match == "prefix" {
		where += " AND (substr(i.name_fold,1,length(?))=? OR substr(i.path_fold,1,length(?))=?)"
		args = append(args, q.Q, q.Q, q.Q, q.Q)
	} else if q.Q != "" {
		where += " AND (instr(i.name_fold,?)>0 OR instr(i.path_fold,?)>0)"
		args = append(args, q.Q, q.Q)
	}
	if q.Category != "" {
		where += " AND i.category=?"
		args = append(args, q.Category)
	}
	if len(q.Categories) > 0 {
		where += " AND i.category IN (" + strings.TrimSuffix(strings.Repeat("?,", len(q.Categories)), ",") + ")"
		for _, category := range q.Categories {
			args = append(args, category)
		}
	}
	if q.Extensions != "" {
		extensions := strings.Split(q.Extensions, ",")
		where += " AND i.extension IN (" + strings.TrimSuffix(strings.Repeat("?,", len(extensions)), ",") + ")"
		for _, ext := range extensions {
			args = append(args, ext)
		}
	}
	if q.MinSize != nil {
		where += " AND i.size>=?"
		args = append(args, *q.MinSize)
	}
	if q.MaxSize != nil {
		where += " AND i.size<=?"
		args = append(args, *q.MaxSize)
	}
	if q.ModifiedAfter != nil {
		where += " AND julianday(i.modified_at)>=julianday(?)"
		args = append(args, stamp(*q.ModifiedAfter))
	}
	if q.ModifiedBefore != nil {
		where += " AND julianday(i.modified_at)<=julianday(?)"
		args = append(args, stamp(*q.ModifiedBefore))
	}
	copyQuery := q
	copyQuery.Cursor = ""
	binding := fingerprint([]any{p.CreatorID, profiles.LeaseID(p), visible, copyQuery})
	// Revision and page rows are read in one SQLite snapshot.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SearchPage{}, err
	}
	defer tx.Rollback()
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT revision FROM scan_index_revision WHERE id=1").Scan(&revision); err != nil {
		return SearchPage{}, err
	}
	if q.Cursor != "" {
		c, err := s.verify(q.Cursor, "search", binding)
		if err != nil || c.Revision != revision {
			return SearchPage{}, ErrSnapshotExpired
		}
		if err = tx.QueryRowContext(ctx, "SELECT natural_key FROM scan_index WHERE profile_id=? AND entry_id=? AND generation=(SELECT generation FROM profiles WHERE id=?)", c.ProfileID, c.ID, c.ProfileID).Scan(&c.Key); err != nil {
			return SearchPage{}, ErrSnapshotExpired
		}
		where += " AND (i.natural_key>? OR (i.natural_key=? AND i.profile_id>?) OR (i.natural_key=? AND i.profile_id=? AND i.entry_id>?))"
		args = append(args, c.Key, c.Key, c.ProfileID, c.Key, c.ProfileID, c.ID)
	}
	args = append(args, q.Limit+1)
	rows, err := tx.QueryContext(ctx, "SELECT i.metadata,i.natural_key FROM scan_index i WHERE "+where+" ORDER BY i.natural_key,i.profile_id,i.entry_id LIMIT ?", args...)
	if err != nil {
		return SearchPage{}, err
	}
	result := SearchPage{Entries: []listing.Entry{}, Coverage: []Coverage{}}
	for rows.Next() {
		var encoded, key string
		if err = rows.Scan(&encoded, &key); err != nil {
			rows.Close()
			return result, err
		}
		var entry listing.Entry
		if err = json.Unmarshal([]byte(encoded), &entry); err != nil {
			rows.Close()
			return result, err
		}
		result.Entries = append(result.Entries, entry)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	if len(result.Entries) > q.Limit {
		result.Entries = result.Entries[:q.Limit]
		last := result.Entries[q.Limit-1]
		next := s.sign(pageCursor{Kind: "search", Binding: binding, Revision: revision, ProfileID: last.ProfileID, ID: last.ID})
		result.NextCursor = &next
	}
	for _, profile := range visible {
		coverage, err := s.coverage(ctx, profile, q.DirectoryID, rootPath)
		if err != nil {
			return result, err
		}
		result.Coverage = append(result.Coverage, coverage)
	}
	return result, nil
}

func (s *Service) coverage(ctx context.Context, p profiles.Profile, directoryID, rootPath string) (Coverage, error) {
	if directoryID == "" {
		directoryID = "root"
	}
	coverage := Coverage{ProfileID: p.ID, DirectoryID: directoryID, State: "absent"}
	// An unfinished ancestor contributes only where its frontier records an
	// attempted listing. Merely discovering or excluding a folder is not coverage.
	j, err := decodeJob(s.db.QueryRowContext(ctx, "SELECT "+jobColumns+` FROM scan_jobs
		WHERE profile_id=? AND (substr(root_key,1,length(?))=? OR
			(substr(?,1,length(root_key))=root_key AND (state='completed' OR EXISTS (
				SELECT 1 FROM scan_frontier f WHERE f.scan_id=scan_jobs.id
				AND f.path_key=? AND f.state IN ('done','failed')))))
		ORDER BY (generation=?) DESC,updated_at DESC,rowid DESC LIMIT 1`, p.ID, rootPath, rootPath, rootPath, rootPath, p.Generation))
	if errors.Is(err, ErrNotFound) {
		return coverage, nil
	}
	if err != nil {
		return coverage, err
	}
	coverage.LastScanAt = &j.UpdatedAt
	coverage.State = j.Coverage.State
	jobRoot, _ := url.PathUnescape(j.RootPath)
	if len(j.Input.Categories) > 0 || !strings.HasPrefix(rootPath, jobRoot) {
		coverage.State = "partial"
	}
	if j.Generation != p.Generation || time.Since(j.UpdatedAt) > 24*time.Hour {
		coverage.State = "stale"
	}
	return coverage, nil
}

func (s *Service) Export(ctx context.Context, p auth.Principal, id, format string, w io.Writer) error {
	if format != "json" && format != "csv" {
		return ErrInvalid
	}
	scan, err := s.Get(ctx, p, id)
	if err != nil {
		return err
	}
	var csvWriter *csv.Writer
	if format == "json" {
		coverage, _ := json.Marshal(scan.Coverage)
		if _, err = fmt.Fprintf(w, "{\"coverage\":%s,\"entries\":[", coverage); err != nil {
			return err
		}
	} else {
		csvWriter = csv.NewWriter(w)
		if err = csvWriter.Write([]string{"profile_id", "entry_id", "name", "escaped_path", "kind", "size", "size_exact", "modified_at", "coverage"}); err != nil {
			return err
		}
		csvWriter.Flush()
		if err = csvWriter.Error(); err != nil {
			return err
		}
	}
	after := ""
	first := true
	for {
		if _, err = s.authorized(ctx, p, scan.ProfileID, "browse:read"); err != nil {
			return err
		}
		rows, err := s.db.QueryContext(ctx, "SELECT entry_id,metadata FROM scan_results WHERE scan_id=? AND entry_id>? ORDER BY entry_id LIMIT 500", id, after)
		if err != nil {
			return err
		}
		type item struct{ id, body string }
		batch := make([]item, 0, 500)
		for rows.Next() {
			var row item
			if err = rows.Scan(&row.id, &row.body); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, row := range batch {
			if err = ctx.Err(); err != nil {
				return err
			}
			if format == "json" {
				separator := ""
				if !first {
					separator = ","
				}
				if _, err = io.WriteString(w, separator+row.body); err != nil {
					return err
				}
				first = false
			} else {
				var e listing.Entry
				if err = json.Unmarshal([]byte(row.body), &e); err != nil {
					return err
				}
				size, modified := "", ""
				if e.Size != nil {
					size = strconv.FormatInt(*e.Size, 10)
				}
				if e.ModifiedAt != nil {
					modified = stamp(*e.ModifiedAt)
				}
				fields := []string{e.ProfileID, e.ID, e.Name, e.EscapedPath, e.Kind, size, strconv.FormatBool(e.SizeExact), modified, scan.Coverage.State}
				for i, value := range fields {
					fields[i] = safeCSV(value)
				}
				if err = csvWriter.Write(fields); err != nil {
					return err
				}
			}
			after = row.id
		}
		if csvWriter != nil {
			csvWriter.Flush()
			if err = csvWriter.Error(); err != nil {
				return err
			}
		}
		if len(batch) < 500 {
			break
		}
	}
	if format == "json" {
		_, err = io.WriteString(w, "]}")
	}
	return err
}
func safeCSV(value string) string {
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
	if strings.HasPrefix(trimmed, "=") || strings.HasPrefix(trimmed, "+") || strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "@") {
		return "'" + value
	}
	return value
}
