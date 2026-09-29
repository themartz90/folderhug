package profiles

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/upstream"
)

var (
	ErrInvalid  = errors.New("invalid profile")
	ErrNotFound = errors.New("profile not found")
	ErrLocked   = errors.New("profile credentials required")
	ErrChanged  = errors.New("profile configuration changed")
)

type Profile struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	RootURL         string `json:"root_url"`
	AuthType        string `json:"auth_type"`
	CredentialState string `json:"credential_state"`
	AllowHTTP       bool   `json:"allow_insecure_http"`
	Previews        bool   `json:"previews_enabled"`
	Connections     int    `json:"connections_per_file"`
	Position        int    `json:"position"`
	Generation      int    `json:"-"`
	configuration   context.Context
}

type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

type Input struct {
	Name        string       `json:"name"`
	RootURL     string       `json:"root_url"`
	AuthType    string       `json:"auth_type"`
	Credentials *Credentials `json:"credentials,omitempty"`
	AllowHTTP   bool         `json:"allow_insecure_http"`
	Previews    *bool        `json:"previews_enabled,omitempty"`
	Connections int          `json:"connections_per_file"`
	Position    int          `json:"position"`
}

type lease struct {
	credentials upstream.Credentials
	ctx         context.Context
	cancel      context.CancelFunc
}

type Service struct {
	db       *sql.DB
	vault    *auth.Vault
	changes  sync.RWMutex
	mu       sync.Mutex
	leases   map[string]*lease
	contexts map[string]context.Context
	cancels  map[string]context.CancelFunc
}

func New(db *sql.DB, vault *auth.Vault) *Service {
	return &Service{db: db, vault: vault, leases: make(map[string]*lease), contexts: make(map[string]context.Context), cancels: make(map[string]context.CancelFunc)}
}

func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, l := range s.leases {
		l.cancel()
		delete(s.leases, key)
	}
	for _, cancel := range s.cancels {
		cancel()
	}
}

func LeaseID(p auth.Principal) string {
	if p.Owner {
		return "session:" + p.SessionID
	}
	return "token:" + p.CreatorID
}

func (s *Service) Create(ctx context.Context, in Input, principal auth.Principal) (Profile, error) {
	s.changes.Lock()
	defer s.changes.Unlock()
	if !principal.Owner {
		return Profile{}, ErrInvalid
	}
	preview := true
	if in.Previews != nil {
		preview = *in.Previews
	}
	p := Profile{ID: rand.Text(), Name: strings.TrimSpace(in.Name), RootURL: in.RootURL, AuthType: in.AuthType, AllowHTTP: in.AllowHTTP, Previews: preview, Connections: in.Connections, Position: in.Position, Generation: 1}
	if p.AuthType == "" {
		p.AuthType = "none"
	}
	if p.Connections == 0 {
		p.Connections = 1
	}
	if err := validate(&p, in.Credentials); err != nil {
		return Profile{}, err
	}
	encoded, err := s.encodeCredentials(p, in.Credentials)
	if err != nil {
		return Profile{}, err
	}
	body, _ := json.Marshal(p)
	_, err = s.db.ExecContext(ctx, "INSERT INTO profiles(id,name,position,generation,settings,credentials) VALUES(?,?,?,?,?,?)", p.ID, p.Name, p.Position, p.Generation, string(body), encoded)
	if err != nil {
		return Profile{}, err
	}
	if in.Credentials != nil && !in.Credentials.Remember && p.AuthType == "basic" {
		s.unlock(p.ID, LeaseID(principal), *in.Credentials, principal.ExpiresAt)
	}
	return s.get(ctx, p.ID, LeaseID(principal))
}

func validate(p *Profile, creds *Credentials) error {
	if p.Name == "" || len(p.Name) > 128 || len(p.RootURL) > 4096 || p.Position < 0 || (p.AuthType != "none" && p.AuthType != "basic") || (p.Connections != 1 && p.Connections != 2 && p.Connections != 4) {
		return ErrInvalid
	}
	root, err := upstream.NewRoot(p.RootURL, p.AllowHTTP)
	if err != nil {
		return ErrInvalid
	}
	p.RootURL = root.URL().String()
	if creds != nil && (len(creds.Username) > 1024 || len(creds.Password) > 4096 || strings.ContainsAny(creds.Username, ":\r\n") || strings.ContainsAny(creds.Password, "\r\n")) {
		return ErrInvalid
	}
	return nil
}

func (s *Service) encodeCredentials(p Profile, c *Credentials) ([]byte, error) {
	if p.AuthType != "basic" || c == nil || !c.Remember {
		return nil, nil
	}
	b, _ := json.Marshal(upstream.Credentials{Username: c.Username, Password: c.Password})
	return s.vault.Seal(p.ID, b)
}

func (s *Service) load(ctx context.Context, id string) (Profile, []byte, error) {
	var p Profile
	var b string
	var cipher []byte
	var generation int
	err := s.db.QueryRowContext(ctx, "SELECT settings,credentials,generation FROM profiles WHERE id=?", id).Scan(&b, &cipher, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil, ErrNotFound
	}
	if err != nil {
		return p, nil, err
	}
	if err = json.Unmarshal([]byte(b), &p); err != nil {
		return p, nil, err
	}
	p.Generation = generation
	return p, cipher, nil
}

func (s *Service) Get(ctx context.Context, id, leaseID string) (Profile, error) {
	s.changes.RLock()
	defer s.changes.RUnlock()
	return s.get(ctx, id, leaseID)
}

func (s *Service) get(ctx context.Context, id, leaseID string) (Profile, error) {
	p, cipher, err := s.load(ctx, id)
	if err != nil {
		return p, err
	}
	s.mu.Lock()
	p.configuration = s.profileContextLocked(id)
	s.mu.Unlock()
	p.CredentialState = "not_required"
	if p.AuthType == "basic" {
		p.CredentialState = "locked"
		if len(cipher) > 0 {
			p.CredentialState = "remembered"
		}
		s.mu.Lock()
		l := s.leases[id+"\x00"+leaseID]
		if l != nil && l.ctx.Err() == nil {
			p.CredentialState = "temporary"
		}
		s.mu.Unlock()
	}
	return p, nil
}

func (s *Service) List(ctx context.Context, principal auth.Principal) ([]Profile, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM profiles ORDER BY position,id")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Profile{}
	for _, id := range ids {
		if !principal.Can(id, "browse:read") {
			continue
		}
		p, err := s.Get(ctx, id, LeaseID(principal))
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *Service) Patch(ctx context.Context, id string, patch map[string]json.RawMessage, principal auth.Principal) (Profile, error) {
	if !principal.Owner {
		return Profile{}, ErrInvalid
	}
	s.changes.Lock()
	defer s.changes.Unlock()
	p, cipher, err := s.load(ctx, id)
	if err != nil {
		return p, err
	}
	before := p
	if len(patch) == 0 {
		return p, ErrInvalid
	}
	var creds *Credentials
	credsSet := false
	for key, value := range patch {
		var target any
		switch key {
		case "name":
			target = &p.Name
		case "root_url":
			target = &p.RootURL
		case "auth_type":
			target = &p.AuthType
		case "allow_insecure_http":
			target = &p.AllowHTTP
		case "previews_enabled":
			target = &p.Previews
		case "connections_per_file":
			target = &p.Connections
		case "position":
			target = &p.Position
		case "credentials":
			credsSet = true
			target = &creds
		default:
			return p, ErrInvalid
		}
		if string(value) == "null" && key != "credentials" {
			return p, ErrInvalid
		}
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.DisallowUnknownFields()
		if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
			return p, ErrInvalid
		}
	}
	p.Name = strings.TrimSpace(p.Name)
	if err := validate(&p, creds); err != nil {
		return p, err
	}
	changedSource := p.RootURL != before.RootURL || p.AllowHTTP != before.AllowHTTP
	if changedSource {
		p.Generation++
		cipher = nil
		s.invalidate(id)
	}
	if credsSet || p.AuthType != before.AuthType {
		cipher, err = s.encodeCredentials(p, creds)
		if err != nil {
			return p, err
		}
		s.invalidate(id)
	}
	if p.AuthType == "none" {
		cipher = nil
	}
	body, _ := json.Marshal(p)
	_, err = s.db.ExecContext(ctx, "UPDATE profiles SET name=?,position=?,generation=?,settings=?,credentials=? WHERE id=?", p.Name, p.Position, p.Generation, string(body), cipher, id)
	if err != nil {
		return p, err
	}
	if credsSet && creds != nil && !creds.Remember && p.AuthType == "basic" {
		s.unlock(id, LeaseID(principal), *creds, principal.ExpiresAt)
	}
	return s.get(ctx, id, LeaseID(principal))
}

func (s *Service) Delete(ctx context.Context, id string) error {
	s.changes.Lock()
	defer s.changes.Unlock()
	s.invalidate(id)
	result, err := s.db.ExecContext(ctx, "DELETE FROM profiles WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) invalidate(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, l := range s.leases {
		if strings.HasPrefix(key, id+"\x00") {
			l.cancel()
			delete(s.leases, key)
		}
	}
	if cancel := s.cancels[id]; cancel != nil {
		cancel()
	}
	delete(s.contexts, id)
	delete(s.cancels, id)
}

// Unlock installs credentials only for the configuration returned by Get.
func (s *Service) Unlock(expected Profile, leaseID string, c Credentials, expires time.Time) error {
	s.changes.RLock()
	defer s.changes.RUnlock()
	s.mu.Lock()
	current := s.contexts[expected.ID]
	s.mu.Unlock()
	if expected.configuration == nil || expected.configuration != current || expected.configuration.Err() != nil || expected.AuthType != "basic" {
		return ErrChanged
	}
	if leaseID == "" || !expires.After(time.Now()) {
		return ErrInvalid
	}
	if err := validate(&expected, &c); err != nil {
		return err
	}
	s.unlock(expected.ID, leaseID, c, expires)
	return nil
}

func (s *Service) unlock(id, leaseID string, c Credentials, expires time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := id + "\x00" + leaseID
	if old := s.leases[key]; old != nil {
		old.cancel()
	}
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	l := &lease{upstream.Credentials{Username: c.Username, Password: c.Password}, ctx, cancel}
	s.leases[key] = l
	context.AfterFunc(ctx, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.leases[key] == l {
			delete(s.leases, key)
		}
	})
}

func (s *Service) Lock(id, leaseID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := id + "\x00" + leaseID
	if l := s.leases[key]; l != nil {
		l.cancel()
		delete(s.leases, key)
	}
}

func (s *Service) ClearPrincipal(leaseID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, l := range s.leases {
		if strings.HasSuffix(key, "\x00"+leaseID) {
			l.cancel()
			delete(s.leases, key)
		}
	}
}

// ClearOwnerSessions drops session-only credentials without changing API-token leases.
func (s *Service) ClearOwnerSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, l := range s.leases {
		if strings.Contains(key, "\x00session:") {
			l.cancel()
			delete(s.leases, key)
		}
	}
}

// Access ties upstream work to both profile configuration and its credential lease.
func (s *Service) Access(ctx context.Context, id, leaseID string) (Profile, *upstream.Credentials, context.Context, context.CancelFunc, error) {
	s.changes.RLock()
	defer s.changes.RUnlock()
	p, cipher, err := s.load(ctx, id)
	if err != nil {
		return p, nil, nil, nil, err
	}
	s.mu.Lock()
	profileCtx := s.profileContextLocked(id)
	l := s.leases[id+"\x00"+leaseID]
	var credential *upstream.Credentials
	leaseCtx := profileCtx
	if p.AuthType == "basic" && l != nil && l.ctx.Err() == nil {
		c := l.credentials
		credential = &c
		leaseCtx = l.ctx
	}
	s.mu.Unlock()
	if p.AuthType == "basic" && credential == nil {
		if len(cipher) == 0 {
			return p, nil, nil, nil, ErrLocked
		}
		plain, err := s.vault.Open(id, cipher)
		if err != nil {
			return p, nil, nil, nil, ErrLocked
		}
		var c upstream.Credentials
		if json.Unmarshal(plain, &c) != nil {
			return p, nil, nil, nil, ErrLocked
		}
		credential = &c
	}
	requestCtx, cancel := context.WithCancel(ctx)
	stopProfile := context.AfterFunc(profileCtx, cancel)
	stopLease := context.AfterFunc(leaseCtx, cancel)
	return p, credential, requestCtx, func() { stopProfile(); stopLease(); cancel() }, nil
}

func (s *Service) profileContextLocked(id string) context.Context {
	ctx := s.contexts[id]
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		s.contexts[id], s.cancels[id] = ctx, cancel
	}
	return ctx
}
