package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) (*Manager, Principal, *Store) {
	t.Helper()
	m, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := m.Close(); e != nil {
			t.Error(e)
		}
	})
	token := NewID() + NewID()
	if e = m.Bootstrap(token); e != nil {
		t.Fatal(e)
	}
	p, e := m.Authenticate(token)
	if e != nil {
		t.Fatal(e)
	}
	sp, e := m.CreateSpace(p, Space{ID: "work", Name: "Work"})
	if e != nil {
		t.Fatal(e)
	}
	j, e := m.CreateProject(p, Project{ID: "demo", SpaceID: sp.ID, Name: "Demo"})
	if e != nil {
		t.Fatal(e)
	}
	s, e := m.Store(p, j.ID, "write")
	if e != nil {
		t.Fatal(e)
	}
	return m, p, s
}
func TestIsolationAndRoles(t *testing.T) {
	m, p, s := setup(t)
	ctx := context.Background()
	u, token, e := m.CreateUser(p, "Reader")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Authenticate(token); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Store(u, s.Project.ID, "read"); !errors.Is(e, ErrDenied) {
		t.Fatalf("ungranted access: %v", e)
	}
	if e = m.Grant(p, Grant{UserID: u.ID, ProjectID: s.Project.ID, Role: "reader"}); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Store(u, s.Project.ID, "read"); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Store(u, s.Project.ID, "write"); !errors.Is(e, ErrDenied) {
		t.Fatalf("reader write: %v", e)
	}
	home, e := m.CreateSpace(p, Space{ID: "home", Name: "Home"})
	if e != nil {
		t.Fatal(e)
	}
	private, e := m.CreateProject(p, Project{SpaceID: home.ID, Name: "Private"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Store(u, private.ID, "read"); !errors.Is(e, ErrDenied) {
		t.Fatalf("other space visible: %v", e)
	}
	visible, e := m.Spaces(u)
	if e != nil || len(visible) != 1 {
		t.Fatalf("visible spaces: %v %v", visible, e)
	}
	if _, _, e = s.Put(ctx, p.ID, WriteInput{Entity: "guide", Kind: "note", Title: "Guide", Text: "API password=do-not-store"}); e != nil {
		t.Fatal(e)
	}
	h, e := s.Heads(ctx, "guide")
	if e != nil || h[0].Text != "API password=[REDACTED]" {
		t.Fatalf("redaction: %v %v", h, e)
	}
	if e = m.Grant(p, Grant{UserID: u.ID, ProjectID: s.Project.ID}); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Store(u, s.Project.ID, "read"); !errors.Is(e, ErrDenied) {
		t.Fatalf("revocation ignored: %v", e)
	}
}
func TestRevisionsAndConcurrentMerge(t *testing.T) {
	_, p, s := setup(t)
	ctx := context.Background()
	first, _, e := s.Put(ctx, p.ID, WriteInput{Entity: "design", Kind: "note", Title: "Design", Text: "original"})
	if e != nil {
		t.Fatal(e)
	}
	second, _, e := s.Put(ctx, p.ID, WriteInput{Entity: "design", Kind: "note", Title: "Design", Text: "second", Expected: []string{first.ID}})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.Put(ctx, p.ID, WriteInput{Entity: "design", Kind: "note", Title: "Design", Text: "stale", Expected: []string{first.ID}}); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale write accepted: %v", e)
	}
	offline := first
	offline.ID = NewID()
	offline.Parents = []string{first.ID}
	offline.Text = "offline"
	offline.Created = Now()
	if n, e := s.Merge(ctx, []Record{offline}); e != nil || n != 1 {
		t.Fatalf("merge: %d %v", n, e)
	}
	if n, e := s.Merge(ctx, []Record{offline}); e != nil || n != 0 {
		t.Fatalf("retry: %d %v", n, e)
	}
	h, e := s.Heads(ctx, "design")
	if e != nil || len(h) != 2 {
		t.Fatalf("lost concurrent write: %v %v", h, e)
	}
	resolved, e := s.Resolve(ctx, p.ID, "design", second.ID, "confirmed by maintainer", []string{second.ID, offline.ID})
	if e != nil {
		t.Fatal(e)
	}
	h, e = s.Heads(ctx, "design")
	if e != nil || len(h) != 1 || h[0].ID != resolved.ID {
		t.Fatalf("resolve: %v %v", h, e)
	}
	history, e := s.History(ctx, "design")
	if e != nil || len(history) != 4 {
		t.Fatalf("history lost: %d %v", len(history), e)
	}
	target, e := OpenStore(filepath.Join(t.TempDir(), "memory.db"), s.Project)
	if e != nil {
		t.Fatal(e)
	}
	if n, e := target.Merge(ctx, history); e != nil || n != 4 {
		t.Fatalf("reverse delivery: %d %v", n, e)
	}
	if e = target.Close(); e != nil {
		t.Fatal(e)
	}
	target, e = OpenStore(target.Path, s.Project)
	if e != nil {
		t.Fatal(e)
	}
	defer target.Close()
	h, e = target.Heads(ctx, "design")
	if e != nil || len(h) != 1 || h[0].Text != "second" {
		t.Fatalf("reopen: %v %v", h, e)
	}
	bad := offline
	bad.Text = "tampered"
	if _, e = target.Merge(ctx, []Record{bad}); e == nil {
		t.Fatal("revision collision accepted")
	}
}
func TestMergeRejectsCyclesAndCrossEntityParents(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cross bool
	}{{"cycle", false}, {"cross entity", true}} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, s := setup(t)
			a := Record{ID: NewID(), Entity: "x", Kind: "note", Title: "A", Created: Now()}
			b := a
			b.ID = NewID()
			a.Parents = []string{b.ID}
			if tc.cross {
				b.Entity = "y"
			} else {
				b.Parents = []string{a.ID}
			}
			if _, e := s.Merge(context.Background(), []Record{a, b}); e == nil {
				t.Fatal("invalid graph accepted")
			}
			n, e := s.Revision(context.Background())
			if e != nil || n != 0 {
				t.Fatalf("partial transaction: %d %v", n, e)
			}
		})
	}
}
