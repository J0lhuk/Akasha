package syncer

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"akasha/internal/core"
	"akasha/internal/skillrepo"
)

func TestSharedSkillsTravelBetweenDevices(t *testing.T) {
	ctx := context.Background()
	token := core.NewID() + core.NewID()
	hub, err := NewHub(t.TempDir(), token)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	server := httptest.NewServer(hub)
	defer server.Close()
	client, err := NewClient(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	a, b := newDevice(t), newDevice(t)
	shared := core.SharedID("work")
	sa, err := a.manager.Store(a.actor, shared, "write")
	if err != nil {
		t.Fatal(err)
	}
	sb, err := b.manager.Store(b.actor, shared, "write")
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Enroll(ctx, core.Space{ID: "work", Name: "Work"}, sa.Project); err != nil {
		t.Fatal(err)
	}
	ka, err := a.vault.ProjectKey(sa.Project)
	if err != nil {
		t.Fatal(err)
	}
	pass := []byte("skills transfer test password")
	portable, err := a.vault.Export(sa.Project, pass)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.vault.Import(sb.Project, pass, portable); err != nil {
		t.Fatal(err)
	}
	kb, err := b.vault.ProjectKey(sb.Project)
	if err != nil {
		t.Fatal(err)
	}
	makeSkill := func(text string) string {
		root := t.TempDir()
		if e := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: review\ndescription: Review code in the selected project.\n---\n"+text), 0600); e != nil {
			t.Fatal(e)
		}
		return root
	}
	va, _, err := skillrepo.Publish(ctx, sa, a.actor.ID, makeSkill("branch A"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = skillrepo.Publish(ctx, sb, b.actor.ID, makeSkill("branch B"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Sync(ctx, sa, ka); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Sync(ctx, sb, kb); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Sync(ctx, sa, ka); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*core.Store{sa, sb} {
		v, e := skillrepo.Catalog(ctx, s, "review")
		if e != nil || len(v) != 2 {
			t.Fatal("skills did not converge", v, e)
		}
	}
	result, err := skillrepo.Install(ctx, sb, va.ID, filepath.Join(t.TempDir(), "review"), false)
	if err != nil || !result.Changed {
		t.Fatal("restore remote skill", result, err)
	}
	user, userToken, err := hub.Manager.CreateUser(core.Principal{ID: "owner", Admin: true}, "reader")
	if err != nil {
		t.Fatal(err)
	}
	if err = hub.Manager.Grant(core.Principal{ID: "owner", Admin: true}, core.Grant{UserID: user.ID, ProjectID: shared, Role: "reader"}); err != nil {
		t.Fatal(err)
	}
	reader, err := NewClient(server.URL, userToken)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = skillrepo.Publish(ctx, sb, b.actor.ID, makeSkill("reader tries upload"), "v2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Sync(ctx, sb, kb); err == nil {
		t.Fatal("reader uploaded skill")
	}
}
