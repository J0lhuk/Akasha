package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"akasha/internal/core"
)

func TestAddPreservesConfigurationAndIsIdempotent(t *testing.T) {
	a := testAccess(t)
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	config := []byte("# preserve this comment\nmodel = 'fixture'\n[mcp_servers.other]\ncommand = 'other-tool'\n")
	if err := os.WriteFile(filepath.Join(root, ".codex/config.toml"), config, 0600); err != nil {
		t.Fatal(err)
	}
	foreign := `{"description":"existing","hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo foreign"}]}]}}`
	if err := os.WriteFile(filepath.Join(root, ".codex/hooks.json"), []byte(foreign), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\nfunc Health() bool { return true }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "akasha.exe")
	data := t.TempDir()
	missing, err := Add(ctx, a.Manager, a.Principal, root, "", "", executable, data)
	if err != nil || missing.Status != "needs_space" || missing.Project != nil {
		t.Fatal("unsafe inferred group", missing, err)
	}
	first, err := Add(ctx, a.Manager, a.Principal, root, "work", "Example", executable, data)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || len(first.Backups) != 2 || len(first.ChangedFiles) == 0 {
		t.Fatal("missing onboarding artifacts", first)
	}
	got, err := os.ReadFile(filepath.Join(root, ".codex/config.toml"))
	if err != nil || !strings.HasPrefix(string(got), string(config)) {
		t.Fatal("original config lost", err)
	}
	got, err = os.ReadFile(filepath.Join(root, ".codex/hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var hooks struct {
		Description string           `json:"description"`
		Hooks       map[string][]any `json:"hooks"`
	}
	if err = json.Unmarshal(got, &hooks); err != nil {
		t.Fatal(err)
	}
	if hooks.Description != "existing" || len(hooks.Hooks["Stop"]) != 2 {
		t.Fatal("foreign hooks lost", hooks)
	}
	second, err := Add(ctx, a.Manager, a.Principal, root, "work", "Ignored rename", executable, data)
	if err != nil || second.Created || second.Project.ID != first.Project.ID || len(second.ChangedFiles) != 0 || second.Import.Changed != 0 {
		t.Fatalf("duplicate onboarding: %+v %v", second, err)
	}
	s, err := a.Manager.Store(a.Principal, first.Project.ID, "write")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetBudget(ctx, a.Principal.ID, "implement", 128); err != nil {
		t.Fatal(err)
	}
	bound := Access{a.Manager, a.Principal, first.Project.ID}
	hook, err := bound.Hook(ctx, HookInput{Event: "UserPromptSubmit", CWD: root, Prompt: "Health", Session: "budget"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := core.Tokens(hook.Specific.Context)
	if err != nil || n > 128 {
		t.Fatal("hook did not use saved budget", n, err)
	}
	// A different Akasha definition must cause a no-write failure.
	otherRoot := t.TempDir()
	if err = os.MkdirAll(filepath.Join(otherRoot, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("[mcp_servers.akasha]\ncommand = 'another-akasha'\n")
	if err = os.WriteFile(filepath.Join(otherRoot, ".codex/config.toml"), original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Add(ctx, a.Manager, a.Principal, otherRoot, "work", "", executable, data); err == nil {
		t.Fatal("overwrote foreign Akasha")
	}
	if _, err = os.Stat(filepath.Join(otherRoot, ".akasha")); !os.IsNotExist(err) {
		t.Fatal("partial configuration before validation", err)
	}
	if _, err = a.Manager.ProjectAt(a.Principal, otherRoot); err != core.ErrNotFound {
		t.Fatal("registered despite config conflict", err)
	}
}

func TestAddRootedGroupsAndReader(t *testing.T) {
	a := testAccess(t)
	ctx := context.Background()
	group := t.TempDir()
	_, err := a.Manager.CreateSpace(a.Principal, core.Space{ID: "home", Name: "Home", Root: group})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(group, "project")
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "akasha")
	data := t.TempDir()
	if _, err = Add(ctx, a.Manager, a.Principal, root, "work", "", exe, data); err == nil {
		t.Fatal("crossed group root")
	}
	result, err := Add(ctx, a.Manager, a.Principal, root, "", "", exe, data)
	if err != nil || result.Project.SpaceID != "home" {
		t.Fatal("rooted group selection", result, err)
	}
	reader, _, err := a.Manager.CreateUser(a.Principal, "Reader")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Manager.Grant(a.Principal, core.Grant{UserID: reader.ID, ProjectID: result.Project.ID, Role: "reader"}); err != nil {
		t.Fatal(err)
	}
	if _, err = Add(ctx, a.Manager, reader, root, "", "", exe, data); err != core.ErrDenied {
		t.Fatal("reader configured hooks", err)
	}
	skill := filepath.Join(t.TempDir(), "akasha")
	if err = InstallCommandSkill(skill, exe, data); err != nil {
		t.Fatal(err)
	}
	if err = InstallCommandSkill(skill, exe, data); err != nil {
		t.Fatal("repeat skill install", err)
	}
	if err = InstallCommandSkill(skill, exe, data+"other"); err == nil {
		t.Fatal("silently redirected existing skill")
	}
}
