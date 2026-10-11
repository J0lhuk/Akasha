package sessionhttp

import (
	"akasha/internal/core"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const cookieName = "akasha_session"

func Token(r *http.Request) string {
	if a := r.Header.Get("Authorization"); a != "" {
		if scheme, token, ok := strings.Cut(a, " "); ok && strings.EqualFold(scheme, "Bearer") {
			return strings.TrimSpace(token)
		}
		return ""
	}
	if c, e := r.Cookie(cookieName); e == nil {
		return c.Value
	}
	return ""
}
func SameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	return r.Header.Get("Sec-Fetch-Site") != "cross-site" && (o == "" || o == "http://"+r.Host || o == "https://"+r.Host)
}
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, e error) {
	status := 400
	if errors.Is(e, core.ErrDenied) {
		status = 401
	}
	if errors.Is(e, core.ErrLoginBusy) {
		status = 429
		w.Header().Set("Retry-After", "60")
	}
	send(w, status, map[string]string{"error": e.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("application/json required")
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return errors.New("single JSON value required")
	}
	return nil
}
func Handle(m *core.Manager, w http.ResponseWriter, r *http.Request, prefix string) bool {
	path := strings.TrimPrefix(r.URL.Path, prefix)
	if r.URL.Path == path {
		return false
	}
	if path != "/login" && path != "/logout" && path != "/accounts" && path != "/sessions/revoke" {
		return false
	}
	if !SameOrigin(r) {
		fail(w, core.ErrDenied)
		return true
	}
	if path == "/accounts" && r.Method == "GET" {
		p, e := m.Authenticate(Token(r))
		if e != nil {
			fail(w, e)
			return true
		}
		v, e := m.Accounts(p)
		if e != nil {
			fail(w, e)
		} else {
			send(w, 200, v)
		}
		return true
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return true
	}
	if path == "/login" {
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if e := decode(w, r, &in); e != nil {
			fail(w, e)
			return true
		}
		peer, _, _ := net.SplitHostPort(r.RemoteAddr)
		v, e := m.Login(r.Context(), in.Username, in.Password, peer)
		if e != nil {
			fail(w, e)
			return true
		}
		if prefix == "/api" {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: v.Token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, Expires: time.Unix(v.Expires, 0)})
			v.Token = ""
		}
		send(w, 200, v)
		return true
	}
	p, e := m.Authenticate(Token(r))
	if e != nil {
		fail(w, e)
		return true
	}
	switch path {
	case "/logout":
		e = m.Logout(r.Context(), Token(r))
		http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	case "/accounts":
		var in struct {
			UserID   string `json:"user_id"`
			Name     string `json:"name"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if e = decode(w, r, &in); e != nil {
			fail(w, e)
			return true
		}
		if in.UserID == "" {
			var v core.Principal
			v, e = m.CreateAccount(r.Context(), p, in.Name, in.Username, in.Password)
			if e == nil {
				send(w, 201, v)
				return true
			}
		} else {
			e = m.SetAccount(r.Context(), p, in.UserID, in.Username, in.Password)
		}
	case "/sessions/revoke":
		var in struct {
			UserID  string `json:"user_id"`
			Disable bool   `json:"disable"`
		}
		if e = decode(w, r, &in); e == nil {
			e = m.RevokeSessions(r.Context(), p, in.UserID, in.Disable)
		}
	}
	if e != nil {
		fail(w, e)
	} else {
		send(w, 200, map[string]bool{"ok": true})
	}
	return true
}
