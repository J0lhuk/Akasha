package syncer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"akasha/internal/core"
	"akasha/internal/sessionhttp"
	"akasha/internal/sqldb"
)

type Hub struct {
	Manager        *core.Manager
	db             *sqldb.DB
	metricsDropped atomic.Uint64
	metricsQueue   chan requestMetric
	metricsStop    chan struct{}
	metricsDone    chan struct{}
	metricsClose   sync.Once
}

func NewHub(root, adminToken string) (*Hub, error) {
	m, err := core.Open(filepath.Join(root, "catalog"))
	if err != nil {
		return nil, err
	}
	if err = m.Bootstrap(adminToken); err != nil {
		m.Close()
		return nil, err
	}
	db, err := core.OpenDB(filepath.Join(root, "relay.db"))
	if err != nil {
		m.Close()
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS events(project TEXT NOT NULL,seq INTEGER NOT NULL,id TEXT NOT NULL,body BLOB NOT NULL,digest TEXT NOT NULL,PRIMARY KEY(project,seq),UNIQUE(project,id));`)
	if err != nil {
		m.Close()
		db.Close()
		return nil, err
	}
	return &Hub{Manager: m, db: sqldb.Wrap(db)}, nil
}
func NewPostgresHub(ctx context.Context, root, token, dsn string) (*Hub, error) {
	m, e := core.OpenPostgres(ctx, root, dsn)
	if e != nil {
		return nil, e
	}
	if e = m.Bootstrap(token); e != nil {
		m.Close()
		return nil, e
	}
	h := &Hub{Manager: m, db: m.CatalogDB()}
	h.startMetrics()
	return h, nil
}
func (h *Hub) Close() error {
	if h.db.Postgres {
		h.metricsClose.Do(func() { close(h.metricsStop); <-h.metricsDone })
		return h.Manager.Close()
	}
	return errors.Join(h.db.Close(), h.Manager.Close())
}
func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func Decode(w http.ResponseWriter, r *http.Request, dst any, max int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("single JSON value required")
	}
	return nil
}
func failure(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, core.ErrDenied) {
		status = 403
	}
	if errors.Is(err, core.ErrNotFound) {
		status = 404
	}
	if errors.Is(err, core.ErrConflict) {
		status = 409
	}
	JSON(w, status, map[string]string{"error": err.Error()})
}
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" && r.Method == "GET" {
		JSON(w, 200, map[string]string{"status": "ok", "service": "akasha-hub"})
		return
	}
	if !sessionhttp.SameOrigin(r) {
		failure(w, core.ErrDenied)
		return
	}
	if sessionhttp.Handle(h.Manager, w, r, "/v1") {
		return
	}
	auth := sessionhttp.Token(r)
	if auth == "" {
		failure(w, core.ErrDenied)
		return
	}
	p, err := h.Manager.Authenticate(auth)
	if err != nil {
		failure(w, err)
		return
	}
	if h.db.Postgres {
		started := time.Now()
		rw := &metricWriter{ResponseWriter: w, status: 200}
		w = rw
		defer func() { h.metric(p.ID, r.URL.Path, rw.status, time.Since(started), rw.bytes) }()
	}
	switch r.URL.Path {
	case "/v1/stats":
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		if !p.Admin {
			failure(w, core.ErrDenied)
			return
		}
		v, e := h.stats(r.Context())
		if e != nil {
			failure(w, e)
		} else {
			JSON(w, 200, v)
		}
		return
	case "/v1/catalog":
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		spaces, e := h.Manager.Spaces(p)
		if e != nil {
			failure(w, e)
			return
		}
		items := []Enrollment{}
		for _, s := range spaces {
			s.Root = ""
			projects, e := h.Manager.Projects(p, s.ID)
			if e != nil {
				failure(w, e)
				return
			}
			for _, j := range projects {
				j.Root = ""
				items = append(items, Enrollment{s, j})
			}
		}
		JSON(w, 200, items)
		return
	case "/v1/enroll":
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var in Enrollment
		if err = Decode(w, r, &in, 16384); err != nil {
			failure(w, err)
			return
		}
		in.Space.Root = ""
		in.Project.Root = ""
		j, e := h.Manager.Register(p, in.Space, in.Project)
		if e != nil {
			failure(w, e)
			return
		}
		JSON(w, 200, j)
		return
	case "/v1/users":
		if r.Method == "GET" {
			users, e := h.Manager.Users(p)
			if e != nil {
				failure(w, e)
				return
			}
			JSON(w, 200, users)
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var in struct {
			Name string `json:"name"`
		}
		if err = Decode(w, r, &in, 4096); err != nil {
			failure(w, err)
			return
		}
		user, token, e := h.Manager.CreateUser(p, in.Name)
		if e != nil {
			failure(w, e)
			return
		}
		JSON(w, 201, map[string]any{"user": user, "token": token})
		return
	case "/v1/grants":
		if r.Method == "GET" {
			grants, e := h.Manager.Grants(p, r.URL.Query().Get("project"))
			if e != nil {
				failure(w, e)
				return
			}
			JSON(w, 200, grants)
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var in core.Grant
		if err = Decode(w, r, &in, 4096); err != nil {
			failure(w, err)
			return
		}
		if err = h.Manager.Grant(p, in); err != nil {
			failure(w, err)
			return
		}
		JSON(w, 200, map[string]bool{"ok": true})
		return
	}
	prefix := "/v1/events/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, prefix)
	action := "read"
	if r.Method == "POST" {
		action = "write"
	} else if r.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	project, err := h.Manager.Project(p, id, action)
	if err != nil {
		failure(w, err)
		return
	}
	if r.Method == "GET" {
		after, e := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		if e != nil || after < 0 {
			failure(w, errors.New("invalid sync cursor"))
			return
		}
		page, e := h.read(project, after)
		if e != nil {
			failure(w, e)
			return
		}
		JSON(w, 200, page)
		return
	}
	var events []Envelope
	if err = Decode(w, r, &events, 32<<20); err != nil {
		failure(w, err)
		return
	}
	if len(events) == 0 || len(events) > 100 {
		failure(w, errors.New("batch must contain 1..100 events"))
		return
	}
	added, err := h.append(project, events)
	if err != nil {
		failure(w, err)
		return
	}
	JSON(w, 200, map[string]int{"added": added})
}
func (h *Hub) append(project core.Project, events []Envelope) (int, error) {
	tx, err := h.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if h.db.Postgres {
		if _, err = tx.Exec("SELECT id FROM projects WHERE id=? FOR UPDATE", project.ID); err != nil {
			return 0, err
		}
	}
	var seq int64
	if err = tx.QueryRow("SELECT coalesce(max(seq),0) FROM events WHERE project=?", project.ID).Scan(&seq); err != nil {
		return 0, err
	}
	count := 0
	for _, e := range events {
		if e.Version != 1 || e.ProjectID != project.ID || e.SpaceID != project.SpaceID || core.ValidateID(e.ID) != nil || len(e.Ciphertext) < 28 || len(e.Ciphertext) > 16<<20 {
			return 0, errors.New("invalid encrypted event")
		}
		body, err := json.Marshal(e)
		if err != nil {
			return 0, err
		}
		digest := core.Hash(string(body))
		var old string
		err = tx.QueryRow("SELECT digest FROM events WHERE project=? AND id=?", project.ID, e.ID).Scan(&old)
		if err == nil {
			if digest != old {
				return 0, core.ErrConflict
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		seq++
		if _, err = tx.Exec("INSERT INTO events(project,seq,id,body,digest) VALUES(?,?,?,?,?)", project.ID, seq, e.ID, body, digest); err != nil {
			return 0, err
		}
		count++
	}
	return count, tx.Commit()
}
func (h *Hub) read(project core.Project, after int64) (Page, error) {
	page := Page{Events: []RemoteEvent{}}
	if err := h.db.QueryRow("SELECT coalesce(max(seq),0) FROM events WHERE project=?", project.ID).Scan(&page.Head); err != nil {
		return page, err
	}
	if after > page.Head {
		return page, errors.New("hub history is behind local cursor; restore hub backup or explicitly reset sync state")
	}
	rows, err := h.db.Query("SELECT seq,body FROM events WHERE project=? AND seq>? ORDER BY seq LIMIT 100", project.ID, after)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	size := 0
	for rows.Next() {
		var event RemoteEvent
		var data []byte
		if err = rows.Scan(&event.Sequence, &data); err != nil {
			return page, err
		}
		if size+len(data) > 24<<20 && len(page.Events) > 0 {
			break
		}
		if err = json.Unmarshal(data, &event.Envelope); err != nil {
			return page, err
		}
		page.Events = append(page.Events, event)
		size += len(data)
	}
	return page, rows.Err()
}
