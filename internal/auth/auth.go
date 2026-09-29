package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

var (
	ErrUnauthenticated = errors.New("authentication required")
	ErrInvalid         = errors.New("invalid authentication input")
	ErrAlreadySetup    = errors.New("owner is already configured")
	ErrNotSetup        = errors.New("owner is not configured")
	ErrNotFound        = errors.New("authentication record not found")
)

const SessionLifetime = 24 * time.Hour
const passwordPrefix = "$argon2id$v=19$m=65536,t=3,p=2$"

type Service struct {
	db        *sql.DB
	hashSlot  chan struct{}
	authority sync.RWMutex
}

func New(db *sql.DB) *Service { return &Service{db: db, hashSlot: make(chan struct{}, 1)} }

type Session struct {
	Authenticated bool      `json:"authenticated"`
	CSRFToken     string    `json:"csrf_token"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type Principal struct {
	Owner      bool
	CreatorID  string
	SessionID  string
	CSRFToken  string
	ExpiresAt  time.Time
	ProfileIDs []string
	Scopes     []string
}

func (p Principal) Session() Session { return Session{p.Owner, p.CSRFToken, p.ExpiresAt} }

func (p Principal) Can(profileID, scope string) bool {
	if p.CreatorID == "" || (!p.ExpiresAt.IsZero() && !time.Now().Before(p.ExpiresAt)) {
		return false
	}
	if p.Owner {
		return true
	}
	return profileID != "" && validScope(scope) && contains(p.ProfileIDs, profileID) && contains(p.Scopes, scope)
}

func (p Principal) CanJob(profileID, creatorID, scope string) bool {
	return p.Can(profileID, scope) && (p.Owner || creatorID == p.CreatorID)
}

func validScope(scope string) bool {
	switch scope {
	case "browse:read", "content:read", "scan:write", "downloads:write", "credentials:unlock":
		return true
	}
	return false
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func randomSecret() string         { return base64.RawURLEncoding.EncodeToString(randomBytes(32)) }
func randomBytes(n int) []byte     { b := make([]byte, n); _, _ = rand.Read(b); return b }
func secretHash(raw string) []byte { sum := sha256.Sum256([]byte(raw)); return sum[:] }

func validPassword(password string) bool {
	return utf8.ValidString(password) && utf8.RuneCountInString(password) >= 12 && len(password) <= 1024
}

func (s *Service) passwordWork(ctx context.Context, work func() string) (string, error) {
	select {
	case s.hashSlot <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.hashSlot }()
	result := work()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return result, nil
}

func (s *Service) hashPassword(ctx context.Context, password string) (string, error) {
	if !validPassword(password) {
		return "", ErrInvalid
	}
	return s.passwordWork(ctx, func() string {
		salt := randomBytes(16)
		hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
		return passwordPrefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(hash)
	})
}

func (s *Service) verifyPassword(ctx context.Context, password, encoded string) bool {
	if len(password) > 1024 || !strings.HasPrefix(encoded, passwordPrefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(encoded, passwordPrefix), "$")
	if len(parts) != 2 {
		return false
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[0])
	if err != nil || len(salt) != 16 {
		return false
	}
	expected, err := base64.RawStdEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(expected) != 32 {
		return false
	}
	result, err := s.passwordWork(ctx, func() string {
		actual := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
		if subtle.ConstantTimeCompare(actual, expected) == 1 {
			return "ok"
		}
		return ""
	})
	return err == nil && result == "ok"
}

func (s *Service) Setup(ctx context.Context, password string) error {
	hash, err := s.hashPassword(ctx, password)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO owner_auth(id,password_hash) VALUES(1,?) ON CONFLICT(id) DO NOTHING`, hash)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAlreadySetup
	}
	return nil
}

func (s *Service) Recover(ctx context.Context, password string) error {
	s.authority.Lock()
	defer s.authority.Unlock()
	hash, err := s.hashPassword(ctx, password)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE owner_auth SET password_hash=? WHERE id=1`, hash)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotSetup
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM auth_sessions`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM api_tokens`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) Login(ctx context.Context, password string) (string, Session, error) {
	var hash string
	if err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM owner_auth WHERE id=1`).Scan(&hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", Session{}, ErrUnauthenticated
		}
		return "", Session{}, err
	}
	if !s.verifyPassword(ctx, password, hash) {
		return "", Session{}, ErrUnauthenticated
	}
	raw := randomSecret()
	session := Session{true, randomSecret(), time.Now().UTC().Add(SessionLifetime).Truncate(time.Second)}
	// A recovery between password verification and insertion must not create a session.
	result, err := s.db.ExecContext(ctx, `INSERT INTO auth_sessions(id,secret_hash,csrf_token,expires_at) SELECT ?,?,?,? FROM owner_auth WHERE id=1 AND password_hash=?`, randomSecret(), secretHash(raw), session.CSRFToken, session.ExpiresAt.Unix(), hash)
	if err != nil {
		return "", Session{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", Session{}, err
	}
	if n != 1 {
		return "", Session{}, ErrUnauthenticated
	}
	return raw, session, nil
}

// Authenticate gives an explicitly supplied bearer token precedence over a cookie.
func (s *Service) Authenticate(ctx context.Context, sessionCookie, bearer string) (Principal, error) {
	if bearer != "" {
		return s.authenticateToken(ctx, bearer)
	}
	if sessionCookie == "" || len(sessionCookie) > 128 {
		return Principal{}, ErrUnauthenticated
	}
	var p Principal
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT id,csrf_token,expires_at FROM auth_sessions WHERE secret_hash=? AND expires_at>?`, secretHash(sessionCookie), time.Now().Unix()).Scan(&p.SessionID, &p.CSRFToken, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	p.Owner = true
	p.CreatorID = "owner"
	p.ExpiresAt = time.Unix(expires, 0).UTC()
	return p, nil
}

func (s *Service) Logout(ctx context.Context, rawCookie string) error {
	s.authority.Lock()
	defer s.authority.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE secret_hash=?`, secretHash(rawCookie))
	return err
}
func (s *Service) RevokeSession(ctx context.Context, id string) error {
	s.authority.Lock()
	defer s.authority.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE id=?`, id)
	return err
}

type TokenCreate struct {
	Name       string     `json:"name"`
	ProfileIDs []string   `json:"profile_ids"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	ProfileIDs []string   `json:"profile_ids"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

func uniqueNonempty(values []string) bool {
	if len(values) == 0 || len(values) > 1000 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range values {
		if v == "" || len(v) > 128 || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

func (s *Service) CreateToken(ctx context.Context, input TokenCreate) (Token, string, error) {
	if strings.TrimSpace(input.Name) == "" || utf8.RuneCountInString(input.Name) > 128 || !uniqueNonempty(input.ProfileIDs) || !uniqueNonempty(input.Scopes) {
		return Token{}, "", ErrInvalid
	}
	for _, scope := range input.Scopes {
		if !validScope(scope) {
			return Token{}, "", ErrInvalid
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	var expires any
	var expiry *time.Time
	if input.ExpiresAt != nil {
		v := input.ExpiresAt.UTC().Truncate(time.Second)
		if !v.After(time.Now()) {
			return Token{}, "", ErrInvalid
		}
		expires = v.Unix()
		expiry = &v
	}
	token := Token{randomSecret(), input.Name, append([]string(nil), input.ProfileIDs...), append([]string(nil), input.Scopes...), expiry, now}
	raw := randomSecret()
	profiles, _ := json.Marshal(token.ProfileIDs)
	scopes, _ := json.Marshal(token.Scopes)
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_tokens(id,name,secret_hash,profile_ids,scopes,expires_at,created_at) VALUES(?,?,?,?,?,?,?)`, token.ID, token.Name, secretHash(raw), string(profiles), string(scopes), expires, now.Unix())
	if err != nil {
		return Token{}, "", err
	}
	return token, raw, nil
}

func (s *Service) authenticateToken(ctx context.Context, raw string) (Principal, error) {
	if len(raw) > 128 {
		return Principal{}, ErrUnauthenticated
	}
	var p Principal
	var profiles, scopes string
	var expires sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id,profile_ids,scopes,expires_at FROM api_tokens WHERE secret_hash=? AND (expires_at IS NULL OR expires_at>?)`, secretHash(raw), time.Now().Unix()).Scan(&p.CreatorID, &profiles, &scopes, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	if json.Unmarshal([]byte(profiles), &p.ProfileIDs) != nil || json.Unmarshal([]byte(scopes), &p.Scopes) != nil {
		return Principal{}, ErrUnauthenticated
	}
	if expires.Valid {
		p.ExpiresAt = time.Unix(expires.Int64, 0).UTC()
	}
	return p, nil
}

func (s *Service) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,profile_ids,scopes,expires_at,created_at FROM api_tokens ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []Token{}
	for rows.Next() {
		var token Token
		var profiles, scopes string
		var expires sql.NullInt64
		var created int64
		if err := rows.Scan(&token.ID, &token.Name, &profiles, &scopes, &expires, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(profiles), &token.ProfileIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(scopes), &token.Scopes); err != nil {
			return nil, err
		}
		if expires.Valid {
			expiry := time.Unix(expires.Int64, 0).UTC()
			token.ExpiresAt = &expiry
		}
		token.CreatedAt = time.Unix(created, 0).UTC()
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (s *Service) DeleteToken(ctx context.Context, id string) error {
	s.authority.Lock()
	defer s.authority.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id=?`, id)
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

// Refresh revalidates a principal already obtained through Authenticate.
func (s *Service) Refresh(ctx context.Context, principal Principal) (Principal, error) {
	var p Principal
	if principal.Owner {
		if principal.CreatorID != "owner" || principal.SessionID == "" {
			return p, ErrUnauthenticated
		}
		var expires int64
		err := s.db.QueryRowContext(ctx, `SELECT id,csrf_token,expires_at FROM auth_sessions WHERE id=? AND expires_at>?`, principal.SessionID, time.Now().Unix()).Scan(&p.SessionID, &p.CSRFToken, &expires)
		if errors.Is(err, sql.ErrNoRows) {
			return p, ErrUnauthenticated
		}
		if err != nil {
			return p, err
		}
		p.Owner = true
		p.CreatorID = "owner"
		p.ExpiresAt = time.Unix(expires, 0).UTC()
		return p, nil
	}
	if principal.CreatorID == "" || principal.SessionID != "" {
		return p, ErrUnauthenticated
	}
	var profiles, scopes string
	var expires sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id,profile_ids,scopes,expires_at FROM api_tokens WHERE id=? AND (expires_at IS NULL OR expires_at>?)`, principal.CreatorID, time.Now().Unix()).Scan(&p.CreatorID, &profiles, &scopes, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrUnauthenticated
	}
	if err != nil {
		return p, err
	}
	if json.Unmarshal([]byte(profiles), &p.ProfileIDs) != nil || json.Unmarshal([]byte(scopes), &p.Scopes) != nil {
		return Principal{}, ErrUnauthenticated
	}
	if expires.Valid {
		p.ExpiresAt = time.Unix(expires.Int64, 0).UTC()
	}
	return p, nil
}

// WithLivePrincipal serializes credential installation with principal revocation.
// The callback must not call a revocation method or wait for network input.
func (s *Service) WithLivePrincipal(ctx context.Context, p Principal, install func(Principal) error) error {
	s.authority.RLock()
	defer s.authority.RUnlock()
	current, err := s.Refresh(ctx, p)
	if err != nil {
		return err
	}
	return install(current)
}
