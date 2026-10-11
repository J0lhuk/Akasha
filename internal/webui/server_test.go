package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"akasha/internal/core"
)

func TestHTTPAuthorizationOriginAndCRUD(t *testing.T) {
	m, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	token := core.NewID() + core.NewID()
	if err = m.Bootstrap(token); err != nil {
		t.Fatal(err)
	}
	owner, err := m.Authenticate(token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Register(owner, core.Space{ID: "work", Name: "Work"}, core.Project{ID: "app", SpaceID: "work", Name: "App"}); err != nil {
		t.Fatal(err)
	}
	app := New(m)
	defer app.Close()
	server := httptest.NewServer(app)
	defer server.Close()
	call := func(method, path, credential, origin string, body any) (int, []byte) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, e := http.NewRequest(method, server.URL+path, bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		return res.StatusCode, b
	}
	if status, _ := call("GET", "/api/spaces", "", "", nil); status != 403 {
		t.Fatalf("unauthenticated request: %d", status)
	}
	if status, _ := call("GET", "/api/spaces", token, "https://evil.invalid", nil); status != 403 {
		t.Fatal("foreign origin accepted")
	}
	status, body := call("POST", "/api/projects/app/records", token, "", core.WriteInput{Kind: "note", Entity: "guide", Title: "Guide", Text: "Use PostgreSQL"})
	if status != 200 {
		t.Fatalf("write failed %d: %s", status, body)
	}
	status, body = call("POST", "/api/projects/app/context", token, "", core.ContextRequest{Query: "PostgreSQL", Budget: 500})
	if status != 200 || !strings.Contains(string(body), "PostgreSQL") {
		t.Fatalf("context failed %d: %s", status, body)
	}
	user, userToken, err := m.CreateUser(owner, "Reader")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Grant(owner, core.Grant{UserID: user.ID, ProjectID: "app", Role: "reader"}); err != nil {
		t.Fatal(err)
	}
	if status, _ = call("GET", "/api/projects/app/records", userToken, "", nil); status != 200 {
		t.Fatal("reader cannot read")
	}
	if status, _ = call("POST", "/api/projects/app/records", userToken, "", core.WriteInput{Kind: "note", Title: "Blocked"}); status != 403 {
		t.Fatal("reader can write")
	}
	if _, err = m.Register(owner, core.Space{ID: "home", Name: "Home"}, core.Project{ID: "private", SpaceID: "home", Name: "Private"}); err != nil {
		t.Fatal(err)
	}
	if status, _ = call("GET", "/api/projects/private/records", userToken, "", nil); status != 403 {
		t.Fatal("other project exposed")
	}
	status, body = call("GET", "/", token, "", nil)
	if status != 200 || !strings.Contains(string(body), "Akasha") {
		t.Fatal("embedded application missing")
	}
	request := httptest.NewRequest("GET", "http://evil.invalid/api/spaces", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	if recorder.Code != 403 {
		t.Fatal("DNS rebinding host accepted")
	}
	if errs := m.CheckpointDirty(context.Background()); len(errs) != 0 {
		t.Fatal(errs)
	}
}
