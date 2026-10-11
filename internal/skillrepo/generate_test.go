package skillrepo

import (
	"akasha/internal/core"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateFromEvidenceKeepsVersionsAndGroup(t *testing.T) {
	ctx := context.Background()
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
	if _, e = m.CreateSpace(p, core.Space{ID: "work", Name: "Work"}); e != nil {
		t.Fatal(e)
	}
	project, e := m.CreateProject(p, core.Project{ID: "source", SpaceID: "work", Name: "Source"})
	if e != nil {
		t.Fatal(e)
	}
	s, e := m.Store(p, project.ID, "write")
	if e != nil {
		t.Fatal(e)
	}
	source, _, e := s.Put(ctx, p.ID, core.WriteInput{Kind: "decision", Title: "Retry procedure", Text: "Use an idempotency key for payment retries. Verified by the duplicate-request fixture."})
	if e != nil {
		t.Fatal(e)
	}
	options := CreateOptions{Name: "payment-retries", Description: "Use when implementing retry handling for payment requests.", Query: "Retry procedure", Procedure: "1. Find the request handler.\n2. Reuse the original idempotency key.\n3. Verify duplicate requests with a fixture."}
	first, e := Create(ctx, m, p, project.ID, options)
	if e != nil {
		t.Fatal(e)
	}
	if first.SpaceID != "work" || len(first.SourceRevisions) != 1 || first.SourceRevisions[0] != source.ID {
		t.Fatal(first)
	}
	if _, e = os.Stat(filepath.Join(first.Local.Path, "references", "provenance.json")); e != nil {
		t.Fatal(e)
	}
	repeated, e := Create(ctx, m, p, project.ID, options)
	if e != nil || repeated.Published || repeated.Version.ID != first.Version.ID || repeated.Local.Changed {
		t.Fatal("not idempotent", repeated, e)
	}
	options.Procedure += "\n4. Also verify the timeout path."
	second, e := Create(ctx, m, p, project.ID, options)
	if e != nil || second.Version.SHA256 == first.Version.SHA256 {
		t.Fatal("lost branch", e)
	}
	shared, e := m.Store(p, core.SharedID("work"), "read")
	if e != nil {
		t.Fatal(e)
	}
	versions, e := Catalog(ctx, shared, options.Name)
	if e != nil || len(versions) != 2 {
		t.Fatal("versions", versions, e)
	}
	if hits, e := shared.Search(ctx, "payment", 10); e != nil || len(hits) != 0 {
		t.Fatal("skill leaked into memory evidence", e)
	}
	options.Procedure = "Authorization: Bearer " + strings.Repeat("x", 64)
	if _, e = Create(ctx, m, p, project.ID, options); e == nil {
		t.Fatal("credential accepted")
	}
	reader, _, e := m.CreateUser(p, "reader")
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Grant(p, core.Grant{UserID: reader.ID, ProjectID: project.ID, Role: "reader"}); e != nil {
		t.Fatal(e)
	}
	options.Procedure = "Review the retry fixture."
	if _, e = Create(ctx, m, reader, project.ID, options); e == nil {
		t.Fatal("repository write allowed without a grant")
	}
}
