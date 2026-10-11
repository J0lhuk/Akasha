package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func put(t *testing.T, s *Store, entity, title, text string) Record {
	t.Helper()
	r, _, err := s.Put(context.Background(), "test", WriteInput{Entity: entity, Kind: "note", Title: title, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestContextBudgetIsolationAndCache(t *testing.T) {
	m, p, s := setup(t)
	ctx := context.Background()
	put(t, s, "design", "Архитектура", "Очередь заказов использует NATS JetStream. [[Хранилище]]\n"+strings.Repeat("Подробности реализации очереди и повторов.\n", 300))
	put(t, s, "storage", "Хранилище", "PostgreSQL хранит подтверждённые заказы.")
	home, err := m.CreateSpace(p, Space{ID: "home", Name: "Home"})
	if err != nil {
		t.Fatal(err)
	}
	j, err := m.CreateProject(p, Project{SpaceID: home.ID, Name: "Private"})
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := m.Store(p, j.ID, "write")
	if err != nil {
		t.Fatal(err)
	}
	put(t, hidden, "secret-note", "NATS", "PRIVATE-HOME-SENTINEL")
	for _, budget := range []int{64, 128, 512, 1500} {
		t.Run(fmt.Sprintf("budget_%d", budget), func(t *testing.T) {
			req := ContextRequest{Query: "очередь NATS", Budget: budget, Shared: true}
			packet, err := m.Context(ctx, p, s.Project.ID, req)
			if err != nil {
				t.Fatal(err)
			}
			n, err := Tokens(packet.Text)
			if err != nil || n != packet.Tokens || n > budget {
				t.Fatalf("budget broken: %d/%d %v", n, budget, err)
			}
			if strings.Contains(packet.Text, "PRIVATE-HOME-SENTINEL") {
				t.Fatal("cross-space leak")
			}
			again, err := m.Context(ctx, p, s.Project.ID, req)
			if err != nil || !again.CacheHit || again.Text != packet.Text {
				t.Fatalf("cache: %v %v", again, err)
			}
		})
	}
	req := ContextRequest{Query: "очередь NATS", Budget: 1500}
	before, err := m.Context(ctx, p, s.Project.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, "new", "NATS retry", "Повтор выполняется через 30 секунд.")
	after, err := m.Context(ctx, p, s.Project.ID, req)
	if err != nil || after.CacheHit || before.Revision == after.Revision {
		t.Fatalf("stale cache: %v", err)
	}
	g, err := s.Graph(ctx)
	if err != nil || len(g.Edges) != 1 {
		t.Fatalf("wiki graph: %+v %v", g, err)
	}
	absent, err := m.Context(ctx, p, s.Project.ID, ContextRequest{Query: "unrelatedxyz", Budget: 200})
	if err != nil || len(absent.Selected) != 0 {
		t.Fatalf("invented evidence: %+v %v", absent, err)
	}
}
func TestStructuredContradictions(t *testing.T) {
	m, p, s := setup(t)
	ctx := context.Background()
	var ids []string
	for _, value := range []string{"PostgreSQL", "SQLite"} {
		r, _, err := s.Put(ctx, p.ID, WriteInput{Kind: "fact", Title: "Database", Subject: "orders", Predicate: "database", Value: value, Text: "Deployment record"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	conflicts, err := s.Conflicts(ctx)
	if err != nil || len(conflicts) != 1 || len(conflicts[0].Versions) != 2 {
		t.Fatalf("lost contradictory fact: %+v %v", conflicts, err)
	}
	packet, err := m.Context(ctx, p, s.Project.ID, ContextRequest{Query: "orders database", Budget: 800})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"PostgreSQL", "SQLite", "ALTERNATIVE"} {
		if !strings.Contains(packet.Text, value) {
			t.Fatalf("silently dropped alternative %s", value)
		}
	}
	if _, err = s.Resolve(ctx, p.ID, conflicts[0].Entity, ids[0], "maintainer confirmed PostgreSQL", ids); err != nil {
		t.Fatal(err)
	}
	conflicts, err = s.Conflicts(ctx)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("resolution failed: %+v %v", conflicts, err)
	}
	history, err := s.History(ctx, packet.Selected[0].Entity)
	if err != nil || len(history) != 3 {
		t.Fatalf("history lost: %d %v", len(history), err)
	}
	for _, in := range []WriteInput{{Kind: "fact", Title: "Version", Subject: "service", Predicate: "version", Value: "1", ValidTo: "2025-01-01T00:00:00Z"}, {Kind: "fact", Title: "Version", Subject: "service", Predicate: "version", Value: "2", ValidFrom: "2025-01-01T00:00:00Z"}} {
		if _, _, err = s.Put(ctx, p.ID, in); err != nil {
			t.Fatal(err)
		}
	}
	conflicts, err = s.Conflicts(ctx)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("disjoint validity reported as contradiction: %+v %v", conflicts, err)
	}
}

func TestSearchIdentifierSurvivesLongDocument(t *testing.T) {
	_, _, s := setup(t)
	put(t, s, "long", "Feature policy", "session_cache expires after 180 seconds.\n"+strings.Repeat("Historical compatibility observations about unrelated modules.\n", 300))
	for i := 0; i < 80; i++ {
		put(t, s, fmt.Sprintf("short/%d", i), "Cache feature", "cache feature expiry experiment")
	}
	hits, err := s.Search(context.Background(), "session_cache feature expiry", 5)
	if err != nil || len(hits) == 0 || hits[0].Entity != "long" {
		t.Fatalf("identifier hidden by short documents: %+v %v", hits, err)
	}
}

func TestPromptHistoryKeepsKnowledgeCacheAndGraph(t *testing.T) {
	m, p, s := setup(t)
	ctx := context.Background()
	put(t, s, "policy", "Queue", "Queue retries wait exactly 30 seconds.")
	req := ContextRequest{Query: "Queue retries", Budget: 256}
	before, err := m.Context(ctx, p, s.Project.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		_, _, err = s.Put(ctx, p.ID, WriteInput{Entity: fmt.Sprintf("prompts/%d", i), Kind: "prompt", Title: "Queue retries", Text: "Queue retries Queue retries"})
		if err != nil {
			t.Fatal(err)
		}
	}
	after, err := m.Context(ctx, p, s.Project.ID, req)
	if err != nil || !after.CacheHit || before.Text != after.Text {
		t.Fatalf("history invalidated unchanged evidence: %+v %v", after, err)
	}
	uncached, err := m.Context(ctx, p, s.Project.ID, ContextRequest{Query: "Queue retries", Budget: 257})
	if err != nil || !strings.Contains(uncached.Text, "30 seconds") {
		t.Fatalf("history displaced actual evidence: %+v %v", uncached, err)
	}
	graph, err := s.Graph(ctx)
	if err != nil || len(graph.Nodes) != 1 {
		t.Fatalf("history polluted graph: %+v %v", graph, err)
	}
	if _, err = m.PromptHistory(ctx, p, "", "", 100); err == nil {
		t.Fatal("ungrouped history was accepted")
	}
}
func TestSharedVisibilityAndPromptHistory(t *testing.T) {
	m, p, s := setup(t)
	ctx := context.Background()
	projects, err := m.Projects(p, s.Project.SpaceID)
	if err != nil {
		t.Fatal(err)
	}
	var shared *Store
	for _, j := range projects {
		if j.Shared {
			shared, err = m.Store(p, j.ID, "write")
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	put(t, shared, "conventions", "Build conventions", "BUILD-SHARED-SENTINEL uses Go.")
	u, _, err := m.CreateUser(p, "Developer")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Grant(p, Grant{UserID: u.ID, ProjectID: s.Project.ID, Role: "editor"}); err != nil {
		t.Fatal(err)
	}
	req := ContextRequest{Query: "BUILD SHARED SENTINEL", Budget: 800, Shared: true}
	packet, err := m.Context(ctx, u, s.Project.ID, req)
	if err != nil || strings.Contains(packet.Text, "BUILD-SHARED-SENTINEL") {
		t.Fatal("shared memory exposed without grant", err)
	}
	if err = m.Grant(p, Grant{UserID: u.ID, ProjectID: shared.Project.ID, Role: "reader"}); err != nil {
		t.Fatal(err)
	}
	packet, err = m.Context(ctx, u, s.Project.ID, req)
	if err != nil || !strings.Contains(packet.Text, "BUILD-SHARED-SENTINEL") {
		t.Fatal("shared grant not applied", err)
	}
	if err = m.Grant(p, Grant{UserID: u.ID, ProjectID: shared.Project.ID}); err != nil {
		t.Fatal(err)
	}
	packet, err = m.Context(ctx, u, s.Project.ID, req)
	if err != nil || strings.Contains(packet.Text, "BUILD-SHARED-SENTINEL") {
		t.Fatal("shared data leaked through cache after revoke", err)
	}
	if _, _, err = s.Put(ctx, u.ID, WriteInput{Entity: "prompts/turn1", Kind: "prompt", Title: "Refactor", Text: "Refactor database password=my-sensitive-value"}); err != nil {
		t.Fatal(err)
	}
	history, err := m.PromptHistory(ctx, u, s.Project.SpaceID, "", 100)
	if err != nil || len(history) != 1 || strings.Contains(history[0].Snippet, "my-sensitive-value") {
		t.Fatalf("prompt history: %+v %v", history, err)
	}
}
