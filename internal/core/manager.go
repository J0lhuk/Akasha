package core

import (
	"akasha/internal/embedding"
	"akasha/internal/sqldb"
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

type Manager struct {
	Root            string
	db              *sqldb.DB
	mu              sync.Mutex
	stores          map[string]*Store
	embeddingMu     sync.Mutex
	embeddingEngine *embedding.Engine
}

func OpenDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath}
	q := uri.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func Open(root string) (*Manager, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	db, err := OpenDB(filepath.Join(abs, "catalog.db"))
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS schema_version(version INTEGER NOT NULL); INSERT INTO schema_version SELECT 1 WHERE NOT EXISTS(SELECT 1 FROM schema_version);
 CREATE TABLE IF NOT EXISTS spaces(id TEXT PRIMARY KEY,name TEXT NOT NULL,root TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS projects(id TEXT PRIMARY KEY,space_id TEXT NOT NULL REFERENCES spaces(id),name TEXT NOT NULL,root TEXT NOT NULL,shared INTEGER NOT NULL DEFAULT 0);
 CREATE UNIQUE INDEX IF NOT EXISTS shared_space ON projects(space_id) WHERE shared=1;
 CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,name TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,admin INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS grants(user_id TEXT NOT NULL REFERENCES users(id),project_id TEXT NOT NULL REFERENCES projects(id),role TEXT NOT NULL CHECK(role IN ('reader','editor','admin')),PRIMARY KEY(user_id,project_id));
 CREATE TABLE IF NOT EXISTS audit(seq INTEGER PRIMARY KEY AUTOINCREMENT,created TEXT NOT NULL,actor TEXT NOT NULL,action TEXT NOT NULL,resource TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS accounts(user_id TEXT PRIMARY KEY REFERENCES users(id),username TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,disabled INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS sessions(token_hash TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),created INTEGER NOT NULL,expires INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Manager{Root: abs, db: sqldb.Wrap(db), stores: map[string]*Store{}}, nil
}
func OpenPostgres(ctx context.Context, root, dsn string) (*Manager, error) {
	db, err := sqldb.OpenPostgres(ctx, dsn)
	if err != nil {
		return nil, err
	}
	var revision string
	if err = db.QueryRowContext(ctx, "SELECT version_num FROM alembic_version").Scan(&revision); err != nil || revision != "0001_accounts_timescale" {
		db.Close()
		return nil, errors.New("server schema is missing or incompatible; run Alembic upgrade head")
	}
	return &Manager{Root: root, db: db, stores: map[string]*Store{}}, nil
}
func (m *Manager) CatalogDB() *sqldb.DB { return m.db }
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []error
	for _, s := range m.stores {
		errs = append(errs, s.Close())
	}
	errs = append(errs, m.db.Close())
	if m.embeddingEngine != nil {
		errs = append(errs, m.embeddingEngine.Close())
	}
	return errors.Join(errs...)
}
func (m *Manager) Bootstrap(token string) error {
	if len(token) < 32 {
		return errors.New("administrator token must be at least 32 characters")
	}
	var n int
	if err := m.db.QueryRow("SELECT count(*) FROM users").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := m.db.Exec("INSERT INTO users(id,name,token_hash,admin) VALUES('owner','Owner',?,1) ON CONFLICT(id) DO NOTHING", Hash(token))
	return err
}
func (m *Manager) Authenticate(token string) (Principal, error) {
	var p Principal
	var stored string
	err := m.db.QueryRow("SELECT id,name,admin,token_hash FROM users WHERE token_hash=? AND NOT EXISTS(SELECT 1 FROM accounts a WHERE a.user_id=users.id AND a.disabled=1)", Hash(token)).Scan(&p.ID, &p.Name, &p.Admin, &stored)
	if err != nil || subtle.ConstantTimeCompare([]byte(stored), []byte(Hash(token))) != 1 {
		return m.authenticateSession(token)
	}
	return p, nil
}
func (m *Manager) CreateUser(p Principal, name string) (Principal, string, error) {
	if !p.Admin {
		return Principal{}, "", ErrDenied
	}
	if strings.TrimSpace(name) == "" || len(name) > 200 {
		return Principal{}, "", errors.New("user name required")
	}
	u := Principal{ID: NewID(), Name: name}
	token := NewID() + NewID()
	_, err := m.db.Exec("INSERT INTO users(id,name,token_hash) VALUES(?,?,?)", u.ID, u.Name, Hash(token))
	if err == nil {
		err = m.audit(p, "user.create", u.ID)
	}
	return u, token, err
}
func (m *Manager) Users(p Principal) ([]Principal, error) {
	if !p.Admin {
		return nil, ErrDenied
	}
	rows, err := m.db.Query("SELECT id,name,admin FROM users ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Principal{}
	for rows.Next() {
		var u Principal
		if err = rows.Scan(&u.ID, &u.Name, &u.Admin); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (m *Manager) Role(p Principal, project string) string {
	if p.Admin {
		return "admin"
	}
	var role string
	_ = m.db.QueryRow("SELECT role FROM grants WHERE user_id=? AND project_id=?", p.ID, project).Scan(&role)
	return role
}
func (m *Manager) Authorize(p Principal, project, action string) error {
	role := m.Role(p, project)
	switch action {
	case "read":
		if role != "" {
			return nil
		}
	case "write":
		if role == "editor" || role == "admin" {
			return nil
		}
	case "admin":
		if role == "admin" {
			return nil
		}
	}
	return ErrDenied
}
func (m *Manager) Grant(p Principal, g Grant) error {
	if err := m.Authorize(p, g.ProjectID, "admin"); err != nil {
		return err
	}
	if g.UserID == p.ID {
		return errors.New("cannot change your own project grant")
	}
	var err error
	if g.Role == "" {
		_, err = m.db.Exec("DELETE FROM grants WHERE user_id=? AND project_id=?", g.UserID, g.ProjectID)
	} else {
		if g.Role != "reader" && g.Role != "editor" && g.Role != "admin" {
			return errors.New("invalid role")
		}
		_, err = m.db.Exec("INSERT INTO grants(user_id,project_id,role) VALUES(?,?,?) ON CONFLICT(user_id,project_id) DO UPDATE SET role=excluded.role", g.UserID, g.ProjectID, g.Role)
	}
	if err == nil {
		err = m.audit(p, "grant."+g.Role, g.ProjectID+":"+g.UserID)
	}
	return err
}
func (m *Manager) Grants(p Principal, project string) ([]Grant, error) {
	if err := m.Authorize(p, project, "admin"); err != nil {
		return nil, err
	}
	rows, err := m.db.Query("SELECT user_id,project_id,role FROM grants WHERE project_id=?", project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	gs := []Grant{}
	for rows.Next() {
		var g Grant
		if err = rows.Scan(&g.UserID, &g.ProjectID, &g.Role); err != nil {
			return nil, err
		}
		gs = append(gs, g)
	}
	return gs, rows.Err()
}
func (m *Manager) audit(p Principal, action, resource string) error {
	_, err := m.db.Exec("INSERT INTO audit(created,actor,action,resource) VALUES(?,?,?,?)", Now(), p.ID, action, resource)
	return err
}

func (m *Manager) CreateSpace(p Principal, s Space) (Space, error) {
	if !p.Admin {
		return s, ErrDenied
	}
	if s.ID == "" {
		s.ID = NewID()
	}
	if err := ValidateID(s.ID); err != nil {
		return s, err
	}
	if s.Name == "" {
		return s, errors.New("space name required")
	}
	if s.Root != "" {
		var err error
		s.Root, err = filepath.Abs(s.Root)
		if err != nil {
			return s, err
		}
		s.Root, err = filepath.EvalSymlinks(s.Root)
		if err != nil {
			return s, err
		}
		rows, err := m.db.Query("SELECT root FROM spaces WHERE root<>''")
		if err != nil {
			return s, err
		}
		for rows.Next() {
			var other string
			if err = rows.Scan(&other); err != nil {
				rows.Close()
				return s, err
			}
			if Within(other, s.Root) || Within(s.Root, other) {
				rows.Close()
				return s, errors.New("space roots must not overlap")
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return s, err
		}
	}
	tx, err := m.db.Begin()
	if err != nil {
		return s, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO spaces(id,name,root) VALUES(?,?,?)", s.ID, s.Name, s.Root); err != nil {
		return s, err
	}
	shared := SharedID(s.ID)
	if _, err = tx.Exec("INSERT INTO projects(id,space_id,name,root,shared) VALUES(?,?,?,'',1)", shared, s.ID, "Общая память"); err != nil {
		return s, err
	}
	if err = tx.Commit(); err != nil {
		return s, err
	}
	return s, m.audit(p, "space.create", s.ID)
}
func (m *Manager) Space(p Principal, id string) (Space, error) {
	ss, err := m.Spaces(p)
	if err != nil {
		return Space{}, err
	}
	for _, s := range ss {
		if s.ID == id {
			return s, nil
		}
	}
	return Space{}, ErrNotFound
}
func (m *Manager) Spaces(p Principal) ([]Space, error) {
	rows, err := m.db.Query("SELECT DISTINCT s.id,s.name,s.root FROM spaces s LEFT JOIN projects j ON j.space_id=s.id LEFT JOIN grants g ON g.project_id=j.id WHERE ?=1 OR g.user_id=? ORDER BY s.name", p.Admin, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Space{}
	for rows.Next() {
		var s Space
		if err = rows.Scan(&s.ID, &s.Name, &s.Root); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (m *Manager) CreateProject(p Principal, j Project) (Project, error) {
	if !p.Admin {
		return j, ErrDenied
	}
	if j.ID == "" {
		j.ID = NewID()
	}
	if err := ValidateID(j.ID); err != nil {
		return j, err
	}
	if j.Name == "" || j.Shared {
		return j, errors.New("project name required; shared project is created with space")
	}
	s, err := m.Space(p, j.SpaceID)
	if err != nil {
		return j, err
	}
	if j.Root != "" {
		j.Root, err = filepath.Abs(j.Root)
		if err != nil {
			return j, err
		}
		j.Root, err = filepath.EvalSymlinks(j.Root)
		if err != nil {
			return j, err
		}
		if s.Root != "" && !Within(s.Root, j.Root) {
			return j, errors.New("project root must be inside its space root")
		}
		rows, e := m.db.Query("SELECT root FROM projects WHERE root<>''")
		if e != nil {
			return j, e
		}
		for rows.Next() {
			var r string
			if e = rows.Scan(&r); e != nil {
				rows.Close()
				return j, e
			}
			if Within(r, j.Root) || Within(j.Root, r) {
				rows.Close()
				return j, errors.New("project roots must not overlap")
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return j, e
		}
	}
	_, err = m.db.Exec("INSERT INTO projects(id,space_id,name,root) VALUES(?,?,?,?)", j.ID, j.SpaceID, j.Name, j.Root)
	if err != nil {
		return j, err
	}
	return j, m.audit(p, "project.create", j.ID)
}
func (m *Manager) Projects(p Principal, space string) ([]Project, error) {
	rows, err := m.db.Query("SELECT DISTINCT j.id,j.space_id,j.name,j.root,j.shared FROM projects j LEFT JOIN grants g ON g.project_id=j.id WHERE j.space_id=? AND (?=1 OR g.user_id=?) ORDER BY j.shared DESC,j.name", space, p.Admin, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var j Project
		if err = rows.Scan(&j.ID, &j.SpaceID, &j.Name, &j.Root, &j.Shared); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (m *Manager) Project(p Principal, id, action string) (Project, error) {
	if err := ValidateID(id); err != nil {
		return Project{}, err
	}
	if err := m.Authorize(p, id, action); err != nil {
		return Project{}, err
	}
	var j Project
	err := m.db.QueryRow("SELECT id,space_id,name,root,shared FROM projects WHERE id=?", id).Scan(&j.ID, &j.SpaceID, &j.Name, &j.Root, &j.Shared)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return j, err
}
func (m *Manager) Store(p Principal, id, action string) (*Store, error) {
	if m.db.Postgres {
		return nil, errors.New("hub stores encrypted events, not local project memory")
	}
	j, err := m.Project(p, id, action)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.stores[id]; s != nil {
		return s, nil
	}
	s, err := OpenStore(filepath.Join(m.Root, "spaces", j.SpaceID, "projects", j.ID, "memory.db"), j)
	if err != nil {
		return nil, err
	}
	m.stores[id] = s
	return s, nil
}
func (m *Manager) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := m.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}
func (m *Manager) SetSetting(ctx context.Context, key, value string) error {
	_, err := m.db.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}
func Within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func SafePath(root, relative string) (string, error) {
	if relative == "" || strings.ContainsAny(relative, "\\:\x00<>\"|?*") || strings.ContainsFunc(relative, func(r rune) bool { return r < 32 }) || strings.HasPrefix(relative, "/") {
		return "", errors.New("unsafe relative path")
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path traversal rejected")
	}
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if strings.TrimRight(part, " .") != part || base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || regexpDevice(base) {
			return "", errors.New("reserved Windows path")
		}
	}
	dst := filepath.Join(root, clean)
	if !Within(root, dst) {
		return "", errors.New("path escape")
	}
	current := root
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symlink path rejected")
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil {
			resolved, e := filepath.EvalSymlinks(current)
			if e != nil || !Within(root, resolved) {
				return "", errors.New("reparse path rejected")
			}
		}
	}
	return dst, nil
}
func regexpDevice(base string) bool {
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9'
}
func Wrap(err error, context string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", context, err)
}

func SharedID(space string) string { return "shared-" + Hash(space)[:32] }

// Register is used for explicit remote metadata enrollment. It never changes an
// existing project's local root or moves it to a different isolation group.
func (m *Manager) Register(p Principal, space Space, project Project) (Project, error) {
	if !p.Admin {
		return Project{}, ErrDenied
	}
	if project.SpaceID != space.ID {
		return Project{}, errors.New("space mismatch")
	}
	if _, err := m.Space(p, space.ID); errors.Is(err, ErrNotFound) {
		if _, err = m.CreateSpace(p, space); err != nil {
			return Project{}, err
		}
	} else if err != nil {
		return Project{}, err
	}
	existing, err := m.Project(p, project.ID, "admin")
	if err == nil {
		if existing.SpaceID != project.SpaceID || existing.Shared != project.Shared {
			return Project{}, errors.New("project metadata collision")
		}
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Project{}, err
	}
	if project.Shared {
		return Project{}, errors.New("shared project id must match its group")
	}
	return m.CreateProject(p, project)
}
