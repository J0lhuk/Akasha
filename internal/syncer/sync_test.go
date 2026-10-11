package syncer

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"akasha/internal/core"
	"akasha/internal/vault"
)

type device struct {
	manager *core.Manager
	actor   core.Principal
	store   *core.Store
	vault   *vault.Vault
}

func newDevice(t *testing.T) device {
	t.Helper()
	data := t.TempDir()
	m, err := core.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e := m.Close(); e != nil {
			t.Error(e)
		}
	})
	token := core.NewID() + core.NewID()
	if err = m.Bootstrap(token); err != nil {
		t.Fatal(err)
	}
	p, err := m.Authenticate(token)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Register(p, core.Space{ID: "work", Name: "Work"}, core.Project{ID: "app", SpaceID: "work", Name: "App", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.Store(p, "app", "write")
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.Unlock(data, []byte("local master password for tests"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	return device{m, p, s, v}
}
func TestTwoDevicesEncryptedSyncFilesSecretsAndConflicts(t *testing.T) {
	ctx := context.Background()
	adminToken := core.NewID() + core.NewID()
	hub, err := NewHub(t.TempDir(), adminToken)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	server := httptest.NewServer(hub)
	defer server.Close()
	client, err := NewClient(server.URL, adminToken)
	if err != nil {
		t.Fatal(err)
	}
	a, b := newDevice(t), newDevice(t)
	if err = client.Enroll(ctx, core.Space{ID: "work", Name: "Work"}, a.store.Project); err != nil {
		t.Fatal(err)
	}
	ka, err := a.vault.ProjectKey(a.store.Project)
	if err != nil {
		t.Fatal(err)
	}
	pass := []byte("transfer key strong test password")
	exported, err := a.vault.Export(a.store.Project, pass)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.vault.Import(b.store.Project, pass, exported); err != nil {
		t.Fatal(err)
	}
	kb, err := b.vault.ProjectKey(b.store.Project)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := a.store.Put(ctx, a.actor.ID, core.WriteInput{Entity: "architecture", Kind: "note", Title: "Architecture", Text: "Use PostgreSQL"})
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("portable-secret-sentinel-847291")
	credential, err := a.vault.Put(ctx, a.store, a.actor.ID, "database", secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(a.store.Project.Root, "app.go"), []byte("package app\n"), 0600); err != nil {
		t.Fatal(err)
	}
	capture, err := Capture(ctx, a.store, a.actor.ID, ka)
	if err != nil || capture.Saved != 1 {
		t.Fatalf("capture: %+v %v", capture, err)
	}
	if _, err = client.Sync(ctx, a.store, ka); err != nil {
		t.Fatal(err)
	}
	report, err := client.Sync(ctx, b.store, kb)
	if err != nil || report.Pulled != 3 {
		t.Fatalf("pull: %+v %v", report, err)
	}
	plain, err := b.vault.Get(ctx, b.store, credential.ID)
	if err != nil || !bytes.Equal(plain, secret) {
		t.Fatal("secret not portable", err)
	}
	hits, err := b.store.Search(ctx, string(secret), 20)
	if err != nil || len(hits) != 0 {
		t.Fatal("secret indexed", err)
	}
	if _, err = Restore(ctx, b.store, kb, true); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(b.store.Project.Root, "app.go"))
	if err != nil || string(restored) != "package app\n" {
		t.Fatal("file not restored", err)
	}
	for _, d := range []device{a, b} {
		if _, _, err = d.store.Put(ctx, d.actor.ID, core.WriteInput{Entity: first.Entity, Kind: "note", Title: "Architecture", Text: "Offline edit " + d.store.Path, Expected: []string{first.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = client.Sync(ctx, a.store, ka); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Sync(ctx, b.store, kb); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Sync(ctx, a.store, ka); err != nil {
		t.Fatal(err)
	}
	for _, d := range []device{a, b} {
		heads, e := d.store.Heads(ctx, first.Entity)
		if e != nil || len(heads) != 2 {
			t.Fatalf("offline update lost: %+v %v", heads, e)
		}
	}
	again, err := client.Sync(ctx, b.store, kb)
	if err != nil || again.Pushed != 0 || again.Pulled != 0 {
		t.Fatalf("sync not idempotent: %+v %v", again, err)
	}
	if err = os.WriteFile(filepath.Join(b.store.Project.Root, "app.go"), []byte("local uncommitted work"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(a.store.Project.Root, "app.go"), []byte("package app\nfunc Updated() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Capture(ctx, a.store, a.actor.ID, ka); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Sync(ctx, a.store, ka); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Sync(ctx, b.store, kb); err != nil {
		t.Fatal(err)
	}
	planned, err := Restore(ctx, b.store, kb, false)
	if err != nil || len(planned.Actions) != 1 || planned.Actions[0].Action != "conflict_copy" {
		t.Fatalf("restore preview: %+v %v", planned, err)
	}
	if _, err = Restore(ctx, b.store, kb, true); err != nil {
		t.Fatal(err)
	}
	restored, err = os.ReadFile(filepath.Join(b.store.Project.Root, "app.go"))
	if err != nil || string(restored) != "local uncommitted work" {
		t.Fatal("local work overwritten", err)
	}
	var body []byte
	if err = hub.db.QueryRow("SELECT body FROM events WHERE project='app' LIMIT 1").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, secret) || bytes.Contains(body, []byte("PostgreSQL")) {
		t.Fatal("hub received plaintext")
	}
	owner, err := hub.Manager.Authenticate(adminToken)
	if err != nil {
		t.Fatal(err)
	}
	reader, readerToken, err := hub.Manager.CreateUser(owner, "Reader")
	if err != nil {
		t.Fatal(err)
	}
	if err = hub.Manager.Grant(owner, core.Grant{UserID: reader.ID, ProjectID: "app", Role: "reader"}); err != nil {
		t.Fatal(err)
	}
	rc, err := NewClient(server.URL, readerToken)
	if err != nil {
		t.Fatal(err)
	}
	if err = rc.Call(ctx, "POST", "/v1/events/app", []Envelope{{}}, nil); err == nil {
		t.Fatal("reader wrote events")
	}
	if err = hub.Manager.Grant(owner, core.Grant{UserID: reader.ID, ProjectID: "app"}); err != nil {
		t.Fatal(err)
	}
	if _, err = rc.Pull(ctx, b.store, kb); err == nil {
		t.Fatal("revoked reader pulled events")
	}
}
func TestEnvelopeTamperAndScope(t *testing.T) {
	p := core.Project{ID: "app", SpaceID: "work"}
	var k vault.Key
	copy(k[:], strings.Repeat("k", 32))
	r := core.Record{ID: core.NewID(), Entity: "note", Kind: "note", Title: "Note", Text: "Evidence", Created: core.Now()}
	envelope, err := Encrypt(k, p, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Decrypt(k, core.Project{ID: "app", SpaceID: "home"}, envelope); err == nil {
		t.Fatal("scope swap accepted")
	}
	wrong := k
	wrong[0] ^= 1
	if _, err = Decrypt(wrong, p, envelope); err == nil {
		t.Fatal("wrong key accepted")
	}
	envelope.Ciphertext[len(envelope.Ciphertext)-1] ^= 1
	if _, err = Decrypt(k, p, envelope); err == nil {
		t.Fatal("tampering accepted")
	}
}
