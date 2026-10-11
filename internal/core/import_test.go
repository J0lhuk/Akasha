package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIncrementalImportCheckpointAndWiki(t *testing.T) {
	_, p, s := setup(t)
	root := t.TempDir()
	s.Project.Root = root
	ctx := context.Background()
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main\n// Hello greets a caller.\nfunc Hello() string { return \"hello\" }\n")
	write("README.md", "# Demo\nUses [[Architecture]].")
	write(".env", "DATABASE_PASSWORD=notindexedsentinel")
	first, err := s.Import(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Files != 2 || first.Changed < 4 {
		t.Fatalf("initial import: %+v", first)
	}
	revision, err := s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Import(ctx, p.ID)
	if err != nil || again.Changed != 0 || again.Deleted != 0 {
		t.Fatalf("not incremental: %+v %v", again, err)
	}
	same, err := s.Revision(ctx)
	if err != nil || same != revision {
		t.Fatal("unchanged import made revisions")
	}
	hits, err := s.Search(ctx, "notindexedsentinel", 10)
	if err != nil || len(hits) != 0 {
		t.Fatal("secret file indexed", err)
	}
	write("main.go", "package main\nfunc Goodbye() string { return \"bye\" }\n")
	if err = os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	changed, err := s.Import(ctx, p.ID)
	if err != nil || changed.Deleted != 2 {
		t.Fatalf("deletion detection: %+v %v", changed, err)
	}
	heads, err := s.Heads(ctx, "symbol/main.go#Hello")
	if err != nil || len(heads) != 1 || !heads[0].Deleted {
		t.Fatalf("stale symbol survived: %+v %v", heads, err)
	}
	restored, err := OpenStore(filepath.Join(t.TempDir(), "memory.db"), s.Project)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	n, err := restored.RestoreCheckpoint(ctx, changed.Checkpoint)
	if err != nil || n == 0 {
		t.Fatalf("restore: %d %v", n, err)
	}
	a, err := s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := restored.Revision(ctx)
	if err != nil || a != b {
		t.Fatalf("restore lost revisions: %d/%d %v", a, b, err)
	}
	put(t, s, "architecture/data", "Architecture", "Use PostgreSQL and [[API]].")
	exported, err := s.ExportWiki(ctx)
	if err != nil || exported.Written < 1 {
		t.Fatal("wiki export", err)
	}
	manifest, err := s.wikiManifest()
	if err != nil {
		t.Fatal(err)
	}
	var name string
	for k, v := range manifest {
		if v.Entity == "architecture/data" {
			name = k
		}
	}
	page := filepath.Join(s.Dir(), "wiki", filepath.FromSlash(name))
	if err = os.WriteFile(page, []byte("# Architecture\n\nUse SQLite and [[API]].\n"), 0600); err != nil {
		t.Fatal(err)
	}
	protected, err := s.ExportWiki(ctx)
	if err != nil || len(protected.Conflicts) != 1 {
		t.Fatalf("external edit overwritten: %+v %v", protected, err)
	}
	imported, err := s.ImportWiki(ctx, p.ID)
	if err != nil || imported.Imported != 1 {
		t.Fatalf("wiki import: %+v %v", imported, err)
	}
	heads, err = s.Heads(ctx, "architecture/data")
	if err != nil || !strings.Contains(heads[0].Text, "SQLite") {
		t.Fatal("wiki edit not stored", err)
	}
}
func TestSafePathAndRootIsolation(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../escape", "a/../../escape", "C:/escape", "/escape", "a\\escape", "NUL.txt", "COM1", "dir/trailing. "} {
		t.Run(name, func(t *testing.T) {
			if _, err := SafePath(root, name); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	m, p, _ := setup(t)
	if _, err := m.CreateSpace(p, Space{Name: "Bound", Root: root}); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateSpace(p, Space{Name: "Overlap", Root: child}); err == nil {
		t.Fatal("overlapping spaces accepted")
	}
}
