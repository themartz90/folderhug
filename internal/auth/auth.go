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

const SessionLifetime = 30 * 24 * time.Hour
const RememberedIdleLifetime = 90 * 24 * time.Hour
const RememberedAbsoluteLifetime = 365 * 24 * time.Hour
const sessionRotationInterval = 24 * time.Hour
const previousSecretGrace = 2 * time.Minute
const passwordPrefix = "$argon2id$v=19$m=65536,t=3,p=2$"

type Service struct {
	db        *sql.DB
	hashSlot  chan struct{}
	authority sync.RWMutex
	now       func() time.Time
}

func New(db *sql.DB) *Service {
	return &Service{db: db, hashSlot: make(chan struct{}, 1), now: time.Now}
}

type Session struct {
	Authenticated   bool      `json:"authenticated"`
	CSRFToken       string    `json:"csrf_token"`
	ExpiresAt       time.Time `json:"expires_at"`
	CookieExpiresAt time.Time `json:"-"`
}

type Principal struct {
	Owner           bool
	CreatorID       string
	SessionID       string
	CSRFToken       string
	ExpiresAt       time.Time
	CookieExpiresAt time.Time
	ProfileIDs      []string
	Scopes          []string
}

func (p Principal) Session() Session {
	return Session{Authenticated: p.Owner, CSRFToken: p.CSRFToken, ExpiresAt: p.ExpiresAt, CookieExpiresAt: p.CookieExpiresAt}
}

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
	return utf8.ValidString(password) && utf8.RuneCountInString(password) >= 6 && len(password) <= 1024
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var complete int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM settings WHERE key='owner_setup_complete'`).Scan(&complete); err != nil {
		return err
	}
	if complete != 0 {
		return ErrAlreadySetup
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO owner_auth(id,password_hash) VALUES(1,?) ON CONFLICT(id) DO NOTHING`, hash)
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('owner_setup_complete','1')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key='owner_setup_pending'`); err != nil {
		return err
	}
	return tx.Commit()
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
	return s.LoginWithRemember(ctx, password, false)
}

func (s *Service) LoginWithRemember(ctx context.Context, password string, remember bool) (string, Session, error) {
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
	now := s.now().UTC().Truncate(time.Second)
	absolute := now.Add(SessionLifetime)
	if remember {
		absolute = now.Add(RememberedAbsoluteLifetime)
	}
	expires := absolute
	if remember {
		expires = now.Add(RememberedIdleLifetime)
	}
	session := Session{Authenticated: true, CSRFToken: randomSecret(), ExpiresAt: expires, CookieExpiresAt: absolute}
	// A recovery between password verification and insertion must not create a session.
	result, err := s.db.ExecContext(ctx, `INSERT INTO auth_sessions(id,secret_hash,csrf_token,expires_at,remembered,absolute_expires_at,last_rotated_at) SELECT ?,?,?,?,?,?,? FROM owner_auth WHERE id=1 AND password_hash=?`, randomSecret(), secretHash(raw), session.CSRFToken, session.ExpiresAt.Unix(), remember, absolute.Unix(), now.Unix(), hash)
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
	var expires, absolute int64
	now := s.now().Unix()
	hash := secretHash(sessionCookie)
	err := s.db.QueryRowContext(ctx, `SELECT id,csrf_token,expires_at,absolute_expires_at FROM auth_sessions WHERE expires_at>? AND (absolute_expires_at>? OR (remembered=0 AND absolute_expires_at=0)) AND (secret_hash=? OR (previous_secret_hash=? AND previous_expires_at>?))`, now, now, hash, hash, now).Scan(&p.SessionID, &p.CSRFToken, &expires, &absolute)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	p.Owner = true
	p.CreatorID = "owner"
	p.ExpiresAt = time.Unix(expires, 0).UTC()
	if absolute == 0 {
		absolute = expires
	}
	p.CookieExpiresAt = time.Unix(absolute, 0).UTC()
	return p, nil
}

func (s *Service) Logout(ctx context.Context, rawCookie string) error {
	s.authority.Lock()
	defer s.authority.Unlock()
	if rawCookie == "" || len(rawCookie) > 128 {
		return nil
	}
	hash := secretHash(rawCookie)
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE secret_hash=? OR previous_secret_hash=?`, hash, hash)
	return err
}

func (s *Service) RevokeAllSessions(ctx context.Context) error {
	s.authority.Lock()
	defer s.authority.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions`)
	return err
}

// RenewSession extends remembered idle time after a real session request. A
// previous cookie remains valid briefly for parallel requests but never replaces
// the browser's newer cookie.
func (s *Service) RenewSession(ctx context.Context, principal Principal, rawCookie string) (string, Session, error) {
	if !principal.Owner || principal.CreatorID != "owner" || principal.SessionID == "" || rawCookie == "" || len(rawCookie) > 128 {
		return "", Session{}, ErrUnauthenticated
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", Session{}, err
	}
	defer tx.Rollback()
	now := s.now().UTC().Truncate(time.Second)
	var csrf string
	var currentHash, previousHash []byte
	var expires, absolute, lastRotation, previousExpires int64
	var remembered bool
	err = tx.QueryRowContext(ctx, `SELECT csrf_token,secret_hash,previous_secret_hash,expires_at,absolute_expires_at,last_rotated_at,previous_expires_at,remembered FROM auth_sessions WHERE id=?`, principal.SessionID).Scan(&csrf, &currentHash, &previousHash, &expires, &absolute, &lastRotation, &previousExpires, &remembered)
	if errors.Is(err, sql.ErrNoRows) {
		return "", Session{}, ErrUnauthenticated
	}
	if err != nil {
		return "", Session{}, err
	}
	hash := secretHash(rawCookie)
	isCurrent := subtle.ConstantTimeCompare(hash, currentHash) == 1
	isPrevious := subtle.ConstantTimeCompare(hash, previousHash) == 1 && previousExpires > now.Unix()
	if (!isCurrent && !isPrevious) || expires <= now.Unix() || (absolute == 0 && remembered) || (absolute != 0 && absolute <= now.Unix()) {
		return "", Session{}, ErrUnauthenticated
	}
	replacement := ""
	if remembered {
		next := now.Add(RememberedIdleLifetime).Unix()
		if next > absolute {
			next = absolute
		}
		if next > expires {
			expires = next
		}
		if isCurrent && now.Unix()-lastRotation >= int64(sessionRotationInterval/time.Second) {
			replacement = randomSecret()
			_, err = tx.ExecContext(ctx, `UPDATE auth_sessions SET secret_hash=?,previous_secret_hash=?,previous_expires_at=?,expires_at=?,last_rotated_at=? WHERE id=?`, secretHash(replacement), currentHash, now.Add(previousSecretGrace).Unix(), expires, now.Unix(), principal.SessionID)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE auth_sessions SET expires_at=? WHERE id=?`, expires, principal.SessionID)
		}
		if err != nil {
			return "", Session{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", Session{}, err
	}
	if absolute == 0 {
		absolute = expires
	}
	return replacement, Session{Authenticated: true, CSRFToken: csrf, ExpiresAt: time.Unix(expires, 0).UTC(), CookieExpiresAt: time.Unix(absolute, 0).UTC()}, nil
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
		var expires, absolute int64
		now := s.now().Unix()
		err := s.db.QueryRowContext(ctx, `SELECT id,csrf_token,expires_at,absolute_expires_at FROM auth_sessions WHERE id=? AND expires_at>? AND (absolute_expires_at>? OR (remembered=0 AND absolute_expires_at=0))`, principal.SessionID, now, now).Scan(&p.SessionID, &p.CSRFToken, &expires, &absolute)
		if errors.Is(err, sql.ErrNoRows) {
			return p, ErrUnauthenticated
		}
		if err != nil {
			return p, err
		}
		p.Owner = true
		p.CreatorID = "owner"
		p.ExpiresAt = time.Unix(expires, 0).UTC()
		if absolute == 0 {
			absolute = expires
		}
		p.CookieExpiresAt = time.Unix(absolute, 0).UTC()
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
