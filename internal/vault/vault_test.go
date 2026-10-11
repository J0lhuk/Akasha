package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"akasha/internal/core"
)

func TestPortableProjectKeysAndWrongPasswords(t *testing.T) {
	root := t.TempDir()
	pass := []byte("long local test password")
	v, err := Unlock(root, pass)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	p := core.Project{ID: "app", SpaceID: "work"}
	key, err := v.ProjectKey(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Unlock(root, []byte("wrong test password")); err == nil {
		t.Fatal("wrong master passphrase accepted")
	}
	reopened, err := Unlock(root, pass)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	same, err := reopened.ProjectKey(p)
	if err != nil || same != key {
		t.Fatal("project key lost on reopen", err)
	}
	transferPass := []byte("portable backup passphrase")
	data, err := v.Export(p, transferPass)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, key[:]) {
		t.Fatal("unencrypted export")
	}
	other, err := Unlock(t.TempDir(), pass)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err = other.Import(p, []byte("wrong transfer passphrase"), data); err == nil {
		t.Fatal("wrong transfer passphrase accepted")
	}
	if err = other.Import(core.Project{ID: "app", SpaceID: "home"}, transferPass, data); err == nil {
		t.Fatal("key imported into different group")
	}
	if err = other.Import(p, transferPass, data); err != nil {
		t.Fatal(err)
	}
	imported, err := other.ProjectKey(p)
	if err != nil || imported != key {
		t.Fatal("key transfer failed", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "vault", "projects", "work", "app.akasha-key"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, key[:]) {
		t.Fatal("plaintext project key on disk")
	}
}
