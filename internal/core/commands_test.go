package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPromptVersionsBudgetsAndCache(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(t.TempDir()+"/memory.db", Project{ID: "commands", SpaceID: "work"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, _, err := s.SavePrompt(ctx, "owner", "review", "Review {{file}}; do not execute {{command}}", nil)
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"file": "сервер.go", "command": "$(delete-all) {{file}}"}
	first, err := s.RenderPrompt(ctx, "review", vars)
	if err != nil || first.CacheHit || !strings.Contains(first.Text, "$(delete-all) {{file}}") {
		t.Fatalf("literal render: %+v %v", first, err)
	}
	second, err := s.RenderPrompt(ctx, "review", vars)
	if err != nil || !second.CacheHit || second.Text != first.Text {
		t.Fatalf("cache: %+v %v", second, err)
	}
	if _, err = s.RenderPrompt(ctx, "review", map[string]string{}); err == nil {
		t.Fatal("missing variables accepted")
	}
	if _, _, err = s.SavePrompt(ctx, "other", "review", "changed", nil); !errors.Is(err, ErrConflict) {
		t.Fatal("unobserved update accepted", err)
	}
	updated, _, err := s.SavePrompt(ctx, "owner", "review", "Changed {{file}}", []string{r.ID})
	if err != nil {
		t.Fatal(err)
	}
	third, err := s.RenderPrompt(ctx, "review", vars)
	if err != nil || third.CacheHit || third.Revision != updated.ID {
		t.Fatal("stale prompt cache", err)
	}
	policy, err := s.SetBudget(ctx, "owner", "implement", 128)
	if err != nil || policy.Budgets["implement"] != 128 {
		t.Fatal("budget", err)
	}
	if _, err = s.SetBudget(ctx, "owner", "implement", 63); err == nil {
		t.Fatal("invalid budget accepted")
	}
	knowledge, err := s.Knowledge(ctx, 100)
	if err != nil || len(knowledge) != 0 {
		t.Fatal("templates or policy leaked into knowledge", knowledge, err)
	}
	if _, err = s.ClearCache(ctx); err != nil {
		t.Fatal(err)
	}
	fourth, err := s.RenderPrompt(ctx, "review", vars)
	if err != nil || fourth.CacheHit {
		t.Fatal("clear cache", err)
	}
	records, err := s.Events(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := OpenStore(t.TempDir()+"/memory.db", s.Project)
	if err != nil {
		t.Fatal(err)
	}
	defer clone.Close()
	toRecords := func(events []Event) []Record {
		out := make([]Record, len(events))
		for i, event := range events {
			out[i] = event.Record
		}
		return out
	}
	if _, err = clone.Merge(ctx, toRecords(records)); err != nil {
		t.Fatal(err)
	}
	clonedPolicy, err := clone.Budgets(ctx)
	if err != nil || clonedPolicy.Budgets["implement"] != 128 {
		t.Fatal("portable policy", err)
	}
	if h, err := clone.History(ctx, "templates/review"); err != nil || len(h) != 2 {
		t.Fatal("prompt versions lost", err)
	}
	// Two offline template edits must remain alternatives after synchronization.
	_, _, err = clone.SavePrompt(ctx, "B", "review", "offline B", []string{updated.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.SavePrompt(ctx, "A", "review", "offline A", []string{updated.ID})
	if err != nil {
		t.Fatal(err)
	}
	records, err = clone.Events(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Merge(ctx, toRecords(records)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RenderPrompt(ctx, "review", nil); !errors.Is(err, ErrConflict) {
		t.Fatal("silently selected template branch", err)
	}
}
