package skillrepo

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"akasha/internal/core"
)

func fixture(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: code-review\ndescription: Review code changes using verified evidence.\n---\n"+content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts/check.ps1"), []byte("throw 'This fixture must never execute'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDivergentSkillVersionsAndSafeInstall(t *testing.T) {
	ctx := context.Background()
	s, err := core.OpenStore(filepath.Join(t.TempDir(), "memory.db"), core.Project{ID: "shared-work", SpaceID: "work", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, b := fixture(t, "Variant A"), fixture(t, "Variant B")
	first, changed, err := Publish(ctx, s, "alice", a, "1.0")
	if err != nil || !changed {
		t.Fatal(first, changed, err)
	}
	again, changed, err := Publish(ctx, s, "alice", a, "1.0")
	if err != nil || changed || first.ID != again.ID {
		t.Fatal("duplicate", again, changed, err)
	}
	second, _, err := Publish(ctx, s, "bob", b, "1.0")
	if err != nil || first.SHA256 == second.SHA256 {
		t.Fatal("lost divergent content", err)
	}
	catalog, err := Catalog(ctx, s, "code-review")
	if err != nil || len(catalog) != 2 {
		t.Fatal("catalog variants", catalog, err)
	}
	hits, err := s.Search(ctx, "Variant", 10)
	if err != nil || len(hits) != 0 {
		t.Fatal("skill leaked into evidence", err)
	}
	destination := filepath.Join(t.TempDir(), "code-review")
	installed, err := Install(ctx, s, first.ID, destination, false)
	if err != nil || !installed.Changed {
		t.Fatal("install", installed, err)
	}
	if _, err = Install(ctx, s, second.ID, destination, false); err == nil {
		t.Fatal("silently replaced skill")
	}
	updated, err := Install(ctx, s, second.ID, destination, true)
	if err != nil || updated.Backup == "" {
		t.Fatal("missing backup", updated, err)
	}
	previous, _, err := Pack(updated.Backup)
	if err != nil || previous.SHA256 != first.SHA256 {
		t.Fatal("previous version lost", err)
	}
	current, _, err := Pack(destination)
	if err != nil || current.SHA256 != second.SHA256 {
		t.Fatal("wrong installed content", err)
	}
	repeated, err := Install(ctx, s, second.ID, destination, false)
	if err != nil || repeated.Changed {
		t.Fatal("repeat install", err)
	}
	if _, err = os.Stat(filepath.Join(destination, "scripts/check.ps1")); err != nil {
		t.Fatal(err)
	}
	// A malformed package cannot escape the destination even if it was relayed
	// by an authenticated user with write access.
	var evil bytes.Buffer
	w := zip.NewWriter(&evil)
	f, err := w.Create("../outside.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("bad"))
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = unpack(evil.Bytes(), "bad"); err == nil {
		t.Fatal("zip traversal accepted")
	}
	r, err := s.Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.Blob[len(r.Blob)/2] ^= 0xff
	if _, _, err = unpack(r.Blob, r.Meta["sha256"]); err == nil {
		t.Fatal("tampered archive accepted")
	}
	if err = os.WriteFile(filepath.Join(a, ".env"), []byte("secret fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = Pack(a); err == nil {
		t.Fatal("secret file published")
	}
}
