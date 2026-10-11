package core

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"golang.org/x/crypto/argon2"
	"regexp"
	"strings"
	"sync"
	"time"
)

var accountName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.@-]{2,79}$`)
var passwordSlots = make(chan struct{}, 2)
var passwordQueue = make(chan struct{}, 32)
var loginMu sync.Mutex

type attempts struct {
	at time.Time
	n  int
}

var loginAttempts = map[string]attempts{}
var ErrLoginBusy = errors.New("too many login attempts; try again later")

type Session struct {
	User    Principal `json:"user"`
	Token   string    `json:"token,omitempty"`
	Expires int64     `json:"expires"`
}
type Account struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
	Admin    bool   `json:"admin"`
}

func (m *Manager) BootstrapAccount(ctx context.Context, token, username, password string) error {
	var n int
	if err := m.db.QueryRow("SELECT count(*) FROM accounts WHERE user_id='owner'").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if username == "" || password == "" {
		return errors.New("first server start requires AKASHA_ADMIN_USERNAME and AKASHA_ADMIN_PASSWORD")
	}
	p, err := m.Authenticate(token)
	if err != nil {
		return err
	}
	return m.SetAccount(ctx, p, "owner", username, password)
}

func passwordHash(ctx context.Context, password string, salt []byte) (string, error) {
	if len(password) < 12 || len(password) > 1024 {
		return "", errors.New("password must contain 12..1024 bytes")
	}
	select {
	case passwordQueue <- struct{}{}:
	default:
		return "", ErrLoginBusy
	}
	defer func() { <-passwordQueue }()
	select {
	case passwordSlots <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-passwordSlots }()
	if salt == nil {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return "", err
		}
	}
	digest := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return "argon2id-v1$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(digest), nil
}
func validCredentials(username, password string) error {
	if !accountName.MatchString(username) {
		return errors.New("username must be 3..80 lowercase letters, digits, ., _, @ or -")
	}
	if len(password) < 12 || len(password) > 1024 {
		return errors.New("password must contain 12..1024 bytes")
	}
	return nil
}
func (m *Manager) CreateAccount(ctx context.Context, p Principal, name, username, password string) (Principal, error) {
	if !p.Admin {
		return Principal{}, ErrDenied
	}
	username = strings.ToLower(strings.TrimSpace(username))
	if err := validCredentials(username, password); err != nil {
		return Principal{}, err
	}
	if strings.TrimSpace(name) == "" || len(name) > 200 {
		return Principal{}, errors.New("display name required")
	}
	encoded, err := passwordHash(ctx, password, nil)
	if err != nil {
		return Principal{}, err
	}
	u := Principal{ID: NewID(), Name: name}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return u, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO users(id,name,token_hash,admin) VALUES(?,?,?,0)", u.ID, u.Name, Hash(NewID()+NewID())); err != nil {
		return u, err
	}
	if _, err = tx.Exec("INSERT INTO accounts(user_id,username,password_hash,disabled) VALUES(?,?,?,0)", u.ID, username, encoded); err != nil {
		return u, errors.New("account name is unavailable")
	}
	if err = tx.Commit(); err != nil {
		return u, err
	}
	return u, m.audit(p, "account.create", u.ID)
}

// SetAccount is an administrator password reset; it revokes every login session
// and rotates the user's service token in the same transaction.
func (m *Manager) SetAccount(ctx context.Context, p Principal, user, username, password string) error {
	if !p.Admin {
		return ErrDenied
	}
	username = strings.ToLower(strings.TrimSpace(username))
	if err := validCredentials(username, password); err != nil {
		return err
	}
	encoded, err := passwordHash(ctx, password, nil)
	if err != nil {
		return err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO accounts(user_id,username,password_hash,disabled) VALUES(?,?,?,0) ON CONFLICT(user_id) DO UPDATE SET username=excluded.username,password_hash=excluded.password_hash,disabled=0", user, username, encoded); err != nil {
		return errors.New("account cannot be updated")
	}
	if _, err = tx.Exec("DELETE FROM sessions WHERE user_id=?", user); err != nil {
		return err
	}
	// Keep the local owner's DPAPI-protected recovery token usable. Other users'
	// previously issued service credentials are revoked on password reset.
	if user != "owner" {
		if _, err = tx.Exec("UPDATE users SET token_hash=? WHERE id=?", Hash(NewID()+NewID()), user); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return m.audit(p, "account.reset", user)
}
func (m *Manager) Accounts(p Principal) ([]Account, error) {
	if !p.Admin {
		return nil, ErrDenied
	}
	rows, err := m.db.Query("SELECT u.id,a.username,u.name,a.disabled,u.admin FROM accounts a JOIN users u ON u.id=a.user_id ORDER BY a.username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		if err = rows.Scan(&a.UserID, &a.Username, &a.Name, &a.Disabled, &a.Admin); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func allowLogin(key string, limit int) bool {
	loginMu.Lock()
	defer loginMu.Unlock()
	now := time.Now()
	a := loginAttempts[key]
	if now.Sub(a.at) > time.Minute {
		a = attempts{at: now}
	}
	a.n++
	if len(loginAttempts) >= 4096 {
		for k, v := range loginAttempts {
			if now.Sub(v.at) > time.Minute {
				delete(loginAttempts, k)
			}
		}
		if len(loginAttempts) >= 4096 {
			return false
		}
	}
	loginAttempts[key] = a
	return a.n <= limit
}
func (m *Manager) Login(ctx context.Context, username, password, peer string) (Session, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) > 80 || len(password) > 1024 {
		return Session{}, ErrDenied
	}
	// Peer is the socket address supplied by the server; forwarded headers are
	// deliberately not trusted. A separate account bucket stops IP rotation.
	if !allowLogin("peer:"+peer, 60) || !allowLogin("account:"+m.Root+username, 12) {
		return Session{}, ErrLoginBusy
	}
	var u Principal
	var stored string
	var disabled bool
	err := m.db.QueryRowContext(ctx, "SELECT u.id,u.name,u.admin,a.password_hash,a.disabled FROM accounts a JOIN users u ON u.id=a.user_id WHERE a.username=?", username).Scan(&u.ID, &u.Name, &u.Admin, &stored, &disabled)
	parts := strings.Split(stored, "$")
	salt := make([]byte, 16)
	valid := err == nil && len(parts) == 3 && parts[0] == "argon2id-v1"
	if valid {
		salt, err = base64.RawStdEncoding.DecodeString(parts[1])
		valid = err == nil && len(salt) == 16
	}
	candidate := password
	if len(candidate) < 12 {
		candidate = "invalid-padding-password"
	}
	hashed, e := passwordHash(ctx, candidate, salt)
	if e != nil {
		return Session{}, e
	}
	if !valid || disabled || len(password) < 12 || subtle.ConstantTimeCompare([]byte(stored), []byte(hashed)) != 1 {
		return Session{}, ErrDenied
	}
	session := Session{User: u, Token: NewID() + NewID(), Expires: time.Now().Add(12 * time.Hour).Unix()}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	// Lock the credential row on PostgreSQL so a concurrent reset/disable cannot
	// race into issuing a session after revocation.
	if m.db.Postgres {
		if _, err = tx.Exec("SELECT user_id FROM accounts WHERE user_id=? FOR UPDATE", u.ID); err != nil {
			return Session{}, err
		}
	}
	var current string
	var off bool
	if err = tx.QueryRow("SELECT password_hash,disabled FROM accounts WHERE user_id=?", u.ID).Scan(&current, &off); err != nil || off || current != stored {
		return Session{}, ErrDenied
	}
	if _, err = tx.Exec("DELETE FROM sessions WHERE expires<?", time.Now().Unix()); err != nil {
		return Session{}, err
	}
	if _, err = tx.Exec("DELETE FROM sessions WHERE user_id=? AND token_hash NOT IN (SELECT token_hash FROM sessions WHERE user_id=? ORDER BY created DESC,token_hash DESC LIMIT 19)", u.ID, u.ID); err != nil {
		return Session{}, err
	}
	if _, err = tx.Exec("INSERT INTO sessions(token_hash,user_id,created,expires) VALUES(?,?,?,?)", Hash(session.Token), u.ID, time.Now().Unix(), session.Expires); err != nil {
		return Session{}, err
	}
	if err = tx.Commit(); err != nil {
		return Session{}, err
	}
	return session, nil
}
func (m *Manager) authenticateSession(token string) (Principal, error) {
	var p Principal
	if len(token) != 64 {
		return p, ErrDenied
	}
	err := m.db.QueryRow("SELECT u.id,u.name,u.admin FROM sessions s JOIN users u ON u.id=s.user_id JOIN accounts a ON a.user_id=u.id WHERE s.token_hash=? AND s.expires>? AND a.disabled=0", Hash(token), time.Now().Unix()).Scan(&p.ID, &p.Name, &p.Admin)
	if err != nil {
		return Principal{}, ErrDenied
	}
	return p, nil
}
func (m *Manager) Logout(ctx context.Context, token string) error {
	_, err := m.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=?", Hash(token))
	return err
}
func (m *Manager) RevokeSessions(ctx context.Context, p Principal, user string, disable bool) error {
	if !p.Admin && p.ID != user {
		return ErrDenied
	}
	if disable && (!p.Admin || user == p.ID || user == "owner") {
		return ErrDenied
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if m.db.Postgres {
		if _, err = tx.Exec("SELECT user_id FROM accounts WHERE user_id=? FOR UPDATE", user); err != nil {
			return err
		}
	}
	if disable {
		if _, err = tx.Exec("UPDATE accounts SET disabled=1 WHERE user_id=?", user); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("DELETE FROM sessions WHERE user_id=?", user); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return m.audit(p, "sessions.revoke", user)
}
