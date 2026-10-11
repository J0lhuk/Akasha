package webui

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"akasha/internal/core"
	"akasha/internal/embedding"
	"akasha/internal/resources"
	"akasha/internal/sessionhttp"
	"akasha/internal/syncer"
	"akasha/internal/vault"
)

//go:embed static/*
var assets embed.FS

type App struct {
	Manager *core.Manager
	mu      sync.Mutex
	vault   *vault.Vault
	static  http.Handler
}

func New(m *core.Manager) *App {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return &App{Manager: m, static: http.FileServer(http.FS(sub))}
}
func (a *App) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.vault != nil {
		a.vault.Close()
		a.vault = nil
	}
}
func fail(w http.ResponseWriter, err error) {
	status := 400
	if errors.Is(err, core.ErrDenied) {
		status = 403
	}
	if errors.Is(err, core.ErrNotFound) {
		status = 404
	}
	if errors.Is(err, core.ErrConflict) {
		status = 409
	}
	syncer.JSON(w, status, map[string]string{"error": err.Error()})
}
func method(w http.ResponseWriter, r *http.Request, allowed string) bool {
	if r.Method != allowed {
		w.WriteHeader(405)
		return false
	}
	return true
}
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	host := r.Host
	if h, _, e := net.SplitHostPort(host); e == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		fail(w, core.ErrDenied)
		return
	}
	origin := r.Header.Get("Origin")
	if origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
		fail(w, core.ErrDenied)
		return
	}
	if r.URL.Path == "/healthz" {
		syncer.JSON(w, 200, map[string]string{"status": "ok", "version": "0.2.0"})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		a.static.ServeHTTP(w, r)
		return
	}
	if sessionhttp.Handle(a.Manager, w, r, "/api") {
		return
	}
	auth := sessionhttp.Token(r)
	if auth == "" {
		fail(w, core.ErrDenied)
		return
	}
	p, err := a.Manager.Authenticate(auth)
	if err != nil {
		fail(w, err)
		return
	}
	ctx := r.Context()
	switch r.URL.Path {
	case "/api/resources":
		if !method(w, r, "GET") {
			return
		}
		syncer.JSON(w, 200, map[string]any{"profile": resources.Default(), "vector_cache": core.VectorCacheStats()})
		return
	case "/api/me":
		if !method(w, r, "GET") {
			return
		}
		syncer.JSON(w, 200, p)
		return
	case "/api/spaces":
		if r.Method == "GET" {
			v, e := a.Manager.Spaces(p)
			respond(w, v, e)
			return
		}
		if !method(w, r, "POST") {
			return
		}
		var in core.Space
		if e := syncer.Decode(w, r, &in, 16384); e != nil {
			fail(w, e)
			return
		}
		v, e := a.Manager.CreateSpace(p, in)
		respond(w, v, e)
		return
	case "/api/projects":
		if r.Method == "GET" {
			v, e := a.Manager.Projects(p, r.URL.Query().Get("space"))
			respond(w, v, e)
			return
		}
		if !method(w, r, "POST") {
			return
		}
		var in core.Project
		if e := syncer.Decode(w, r, &in, 16384); e != nil {
			fail(w, e)
			return
		}
		v, e := a.Manager.CreateProject(p, in)
		respond(w, v, e)
		return
	case "/api/users":
		if r.Method == "GET" {
			v, e := a.Manager.Users(p)
			respond(w, v, e)
			return
		}
		if !method(w, r, "POST") {
			return
		}
		var in struct {
			Name string `json:"name"`
		}
		if e := syncer.Decode(w, r, &in, 4096); e != nil {
			fail(w, e)
			return
		}
		u, token, e := a.Manager.CreateUser(p, in.Name)
		respond(w, map[string]any{"user": u, "token": token}, e)
		return
	case "/api/grants":
		if r.Method == "GET" {
			v, e := a.Manager.Grants(p, r.URL.Query().Get("project"))
			respond(w, v, e)
			return
		}
		if !method(w, r, "POST") {
			return
		}
		var in core.Grant
		if e := syncer.Decode(w, r, &in, 4096); e != nil {
			fail(w, e)
			return
		}
		e := a.Manager.Grant(p, in)
		respond(w, map[string]bool{"ok": e == nil}, e)
		return
	case "/api/prompts":
		if !method(w, r, "GET") {
			return
		}
		v, e := a.Manager.PromptHistory(ctx, p, r.URL.Query().Get("space"), r.URL.Query().Get("project"), 100)
		respond(w, v, e)
		return
	case "/api/vault/unlock":
		if !method(w, r, "POST") {
			return
		}
		if !p.Admin {
			fail(w, core.ErrDenied)
			return
		}
		var in struct {
			Passphrase string `json:"passphrase"`
		}
		if e := syncer.Decode(w, r, &in, 8192); e != nil {
			fail(w, e)
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		v, e := vault.Unlock(a.Manager.Root, []byte(in.Passphrase))
		if e != nil {
			fail(w, e)
			return
		}
		if a.vault != nil {
			a.vault.Close()
		}
		a.vault = v
		syncer.JSON(w, 200, map[string]bool{"unlocked": true})
		return
	case "/api/vault/lock":
		if !method(w, r, "POST") {
			return
		}
		if !p.Admin {
			fail(w, core.ErrDenied)
			return
		}
		a.Close()
		syncer.JSON(w, 200, map[string]bool{"locked": true})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/projects/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/api/projects/") || len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	project, resource := parts[0], strings.Join(parts[1:], "/")
	action := "read"
	if r.Method == "POST" && resource != "context" {
		action = "write"
	}
	s, err := a.Manager.Store(p, project, action)
	if err != nil {
		fail(w, err)
		return
	}
	if r.Method != "GET" && r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	switch resource {
	case "vectors":
		if !method(w, r, "GET") {
			return
		}
		v, e := s.VectorStatus(ctx)
		info, modelError := embedding.Info(a.Manager.Root)
		respond(w, map[string]any{"index": v, "installed": modelError == nil, "model": info}, e)
	case "vectors/index":
		if !method(w, r, "POST") {
			return
		}
		v, e := a.Manager.IndexVectors(ctx, p, project)
		respond(w, v, e)
	case "stats":
		if !method(w, r, "GET") {
			return
		}
		counts, e := s.Count(ctx)
		if e != nil {
			fail(w, e)
			return
		}
		revision, e := s.Revision(ctx)
		respond(w, map[string]any{"counts": counts, "revision": revision, "role": a.Manager.Role(p, project), "project": s.Project}, e)
	case "records":
		if r.Method == "GET" {
			kind := r.URL.Query().Get("kind")
			var records []core.Record
			var e error
			if kind == "" {
				records, e = s.Knowledge(ctx, 500)
			} else {
				records, e = s.List(ctx, kind, 500)
			}
			if e != nil {
				fail(w, e)
				return
			}
			respond(w, briefRecords(records), nil)
			return
		}
		var in core.WriteInput
		if e := syncer.Decode(w, r, &in, 2<<20); e != nil {
			fail(w, e)
			return
		}
		switch in.Kind {
		case "note", "fact", "decision", "task":
		default:
			fail(w, errors.New("unsupported editable kind"))
			return
		}
		v, changed, e := s.Put(ctx, p.ID, in)
		respond(w, map[string]any{"record": v, "changed": changed}, e)
	case "record":
		if !method(w, r, "GET") {
			return
		}
		v, e := s.Get(ctx, r.URL.Query().Get("id"))
		if e == nil && (v.Kind == "secret" || v.Kind == "file" || v.Kind == "git_bundle") {
			e = core.ErrDenied
		}
		respond(w, v, e)
	case "search":
		if !method(w, r, "GET") {
			return
		}
		v, e := a.Manager.Search(ctx, p, project, r.URL.Query().Get("q"), r.URL.Query().Get("shared") == "true", 50)
		respond(w, v, e)
	case "graph":
		if !method(w, r, "GET") {
			return
		}
		v, e := s.Graph(ctx)
		respond(w, v, e)
	case "context":
		if !method(w, r, "POST") {
			return
		}
		var in core.ContextRequest
		if e := syncer.Decode(w, r, &in, 16384); e != nil {
			fail(w, e)
			return
		}
		v, e := a.Manager.Context(ctx, p, project, in)
		respond(w, v, e)
	case "conflicts":
		if !method(w, r, "GET") {
			return
		}
		v, e := s.Conflicts(ctx)
		for i := range v {
			v[i].Versions = briefRecords(v[i].Versions)
		}
		respond(w, v, e)
	case "resolve":
		if !method(w, r, "POST") {
			return
		}
		var in struct {
			Entity   string   `json:"entity"`
			Chosen   string   `json:"chosen"`
			Reason   string   `json:"reason"`
			Expected []string `json:"expected"`
		}
		if e := syncer.Decode(w, r, &in, 16384); e != nil {
			fail(w, e)
			return
		}
		v, e := s.Resolve(ctx, p.ID, in.Entity, in.Chosen, in.Reason, in.Expected)
		v.Blob = nil
		respond(w, v, e)
	case "history":
		if !method(w, r, "GET") {
			return
		}
		v, e := s.History(ctx, r.URL.Query().Get("entity"))
		respond(w, briefRecords(v), e)
	case "import":
		if !method(w, r, "POST") {
			return
		}
		v, e := s.Import(ctx, p.ID)
		respond(w, v, e)
	case "checkpoint":
		if !method(w, r, "POST") {
			return
		}
		v, e := s.Checkpoint(ctx, "Web UI")
		respond(w, map[string]string{"path": v}, e)
	case "wiki/export":
		if !method(w, r, "POST") {
			return
		}
		v, e := s.ExportWiki(ctx)
		respond(w, v, e)
	case "wiki/import":
		if !method(w, r, "POST") {
			return
		}
		v, e := s.ImportWiki(ctx, p.ID)
		respond(w, v, e)
	case "transfer":
		if !method(w, r, "POST") {
			return
		}
		var in struct {
			To     string `json:"to"`
			ID     string `json:"id"`
			Reason string `json:"reason"`
		}
		if e := syncer.Decode(w, r, &in, 8192); e != nil {
			fail(w, e)
			return
		}
		v, e := a.Manager.Transfer(ctx, p, project, in.To, in.ID, in.Reason)
		respond(w, v, e)
	case "secrets":
		if !method(w, r, "GET") {
			return
		}
		if !p.Admin {
			fail(w, core.ErrDenied)
			return
		}
		v, e := s.List(ctx, "secret", 200)
		respond(w, briefRecords(v), e)
	case "secret/set", "secret/get", "keys/export", "keys/import", "capture", "restore", "sync":
		if !method(w, r, "POST") {
			return
		}
		if !p.Admin {
			fail(w, core.ErrDenied)
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.vault == nil {
			fail(w, errors.New("Сначала откройте хранилище ключей"))
			return
		}
		a.secureAction(ctx, w, r, p, s, resource)
	default:
		http.NotFound(w, r)
	}
}
func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	syncer.JSON(w, 200, value)
}
func briefRecords(records []core.Record) []core.Record {
	for i := range records {
		records[i].Blob = nil
		rs := []rune(records[i].Text)
		if len(rs) > 280 {
			records[i].Text = string(rs[:280]) + "…"
		}
		if len(records[i].Value) > 1000 {
			records[i].Value = string([]rune(records[i].Value)[:min(250, len([]rune(records[i].Value)))]) + "…"
		}
	}
	return records
}
func (a *App) secureAction(ctx context.Context, w http.ResponseWriter, r *http.Request, p core.Principal, s *core.Store, resource string) {
	switch resource {
	case "secret/set":
		var in struct {
			Name     string   `json:"name"`
			Value    string   `json:"value"`
			Expected []string `json:"expected"`
		}
		if e := syncer.Decode(w, r, &in, 80000); e != nil {
			fail(w, e)
			return
		}
		v, e := a.vault.Put(ctx, s, p.ID, in.Name, []byte(in.Value), in.Expected)
		respond(w, map[string]string{"id": v.ID, "name": v.Title}, e)
		return
	case "secret/get":
		var in struct {
			ID string `json:"id"`
		}
		if e := syncer.Decode(w, r, &in, 2048); e != nil {
			fail(w, e)
			return
		}
		secret, e := a.vault.Get(ctx, s, in.ID)
		defer clear(secret)
		respond(w, map[string]string{"value": string(secret)}, e)
		return
	case "keys/export", "keys/import":
		var in struct {
			Passphrase string `json:"passphrase"`
			Data       string `json:"data,omitempty"`
		}
		if e := syncer.Decode(w, r, &in, 2<<20); e != nil {
			fail(w, e)
			return
		}
		if resource == "keys/export" {
			b, e := a.vault.Export(s.Project, []byte(in.Passphrase))
			respond(w, map[string]string{"data": base64.StdEncoding.EncodeToString(b), "name": s.Project.ID + ".akasha-key"}, e)
		} else {
			b, e := base64.StdEncoding.DecodeString(in.Data)
			if e != nil {
				fail(w, e)
				return
			}
			e = a.vault.Import(s.Project, []byte(in.Passphrase), b)
			respond(w, map[string]bool{"imported": e == nil}, e)
		}
		return
	}
	key, err := a.vault.ProjectKey(s.Project)
	if err != nil {
		fail(w, err)
		return
	}
	defer clear(key[:])
	switch resource {
	case "capture":
		v, e := syncer.Capture(ctx, s, p.ID, key)
		respond(w, v, e)
	case "restore":
		var in struct {
			Apply bool `json:"apply"`
		}
		if e := syncer.Decode(w, r, &in, 1024); e != nil {
			fail(w, e)
			return
		}
		v, e := syncer.Restore(ctx, s, key, in.Apply)
		respond(w, v, e)
	case "sync":
		var in struct {
			URL      string `json:"url"`
			Token    string `json:"token"`
			Username string `json:"username"`
			Password string `json:"password"`
			Enroll   bool   `json:"enroll"`
			PullOnly bool   `json:"pull_only"`
			Auto     bool   `json:"auto"`
		}
		if e := syncer.Decode(w, r, &in, 16384); e != nil {
			fail(w, e)
			return
		}
		if in.Token == "" {
			session, e := syncer.Login(ctx, in.URL, in.Username, in.Password)
			if e != nil {
				fail(w, e)
				return
			}
			in.Token = session.Token
		}
		in.Password = ""
		in.Username = ""
		client, e := syncer.NewClient(in.URL, in.Token)
		if e != nil {
			fail(w, e)
			return
		}
		if in.Enroll {
			sp, e := a.Manager.Space(p, s.Project.SpaceID)
			if e != nil {
				fail(w, e)
				return
			}
			if e = client.Enroll(ctx, sp, s.Project); e != nil {
				fail(w, e)
				return
			}
		}
		var report syncer.SyncReport
		if in.PullOnly {
			report, e = client.Pull(ctx, s, key)
		} else {
			report, e = client.Sync(ctx, s, key)
		}
		if e != nil {
			fail(w, e)
			return
		}
		if in.Auto {
			raw, e := json.Marshal(in)
			if e != nil {
				fail(w, e)
				return
			}
			sealed, e := vault.Seal(key, "akasha/cloud-config/"+s.Project.ID, raw)
			if e != nil {
				fail(w, e)
				return
			}
			if e = s.SetState(ctx, "cloud-config", base64.StdEncoding.EncodeToString(sealed)); e != nil {
				fail(w, e)
				return
			}
		} else if e = s.SetState(ctx, "cloud-config", ""); e != nil {
			fail(w, e)
			return
		}
		respond(w, report, nil)
	}
}

// Maintain snapshots changed stores. Optional auto-sync operates only after a
// successful explicit connection and while the owner's vault is unlocked.
func (a *App) Maintain(ctx context.Context, owner core.Principal, logError func(error)) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, err := range a.Manager.CheckpointDirty(ctx) {
				logError(err)
			}
			a.autoSync(ctx, owner, logError)
		}
	}
}
func (a *App) autoSync(ctx context.Context, owner core.Principal, logError func(error)) {
	if !owner.Admin {
		return
	}
	spaces, err := a.Manager.Spaces(owner)
	if err != nil {
		logError(err)
		return
	}
	for _, sp := range spaces {
		projects, e := a.Manager.Projects(owner, sp.ID)
		if e != nil {
			logError(e)
			continue
		}
		for _, p := range projects {
			s, e := a.Manager.Store(owner, p.ID, "read")
			if e != nil {
				logError(e)
				continue
			}
			config, e := s.State(ctx, "cloud-config")
			if e != nil {
				logError(e)
				continue
			}
			if config == "" {
				continue
			}
			a.mu.Lock()
			if a.vault == nil {
				a.mu.Unlock()
				return
			}
			key, e := a.vault.ProjectKey(p)
			a.mu.Unlock()
			if e != nil {
				logError(e)
				continue
			}
			sealed, e := base64.StdEncoding.DecodeString(config)
			if e != nil {
				logError(e)
				continue
			}
			plain, e := vault.Open(key, "akasha/cloud-config/"+p.ID, sealed)
			if e != nil {
				logError(e)
				continue
			}
			var in struct {
				URL, Token string
				PullOnly   bool `json:"pull_only"`
			}
			e = json.Unmarshal(plain, &in)
			clear(plain)
			if e != nil {
				logError(e)
				continue
			}
			client, e := syncer.NewClient(in.URL, in.Token)
			if e != nil {
				logError(e)
				continue
			}
			if in.PullOnly {
				_, e = client.Pull(ctx, s, key)
			} else {
				_, e = client.Sync(ctx, s, key)
			}
			clear(key[:])
			if e != nil {
				logError(e)
			}
		}
	}
}
