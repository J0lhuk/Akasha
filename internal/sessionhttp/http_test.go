package sessionhttp

import (
	"akasha/internal/core"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPasswordCookieLoginAndLogout(t *testing.T) {
	m, e := core.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	token := core.NewID() + core.NewID()
	if e = m.Bootstrap(token); e != nil {
		t.Fatal(e)
	}
	p, e := m.Authenticate(token)
	if e != nil {
		t.Fatal(e)
	}
	password := core.NewID()
	if e = m.SetAccount(context.Background(), p, p.ID, "fixture-owner", password); e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(map[string]string{"username": "fixture-owner", "password": password})
	r := httptest.NewRequest("POST", "http://localhost/api/login", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "127.0.0.2:9000"
	w := httptest.NewRecorder()
	if !Handle(m, w, r, "/api") || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("cookie security")
	}
	var session core.Session
	if e = json.Unmarshal(w.Body.Bytes(), &session); e != nil || session.Token != "" {
		t.Fatal("UI leaked bearer")
	}
	if _, e = m.Authenticate(cookies[0].Value); e != nil {
		t.Fatal(e)
	}
	r = httptest.NewRequest("POST", "http://localhost/api/logout", strings.NewReader("{}"))
	r.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	Handle(m, w, r, "/api")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if _, e = m.Authenticate(cookies[0].Value); e == nil {
		t.Fatal("session survived logout")
	}
	r = httptest.NewRequest("POST", "http://localhost/api/login", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://other.invalid")
	w = httptest.NewRecorder()
	Handle(m, w, r, "/api")
	if w.Code != 401 {
		t.Fatal("cross-origin login accepted")
	}
}
