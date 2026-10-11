package core

import (
	"akasha/internal/embedding"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// This opt-in test uses the installed, hash-verified native model. Ordinary
// go test remains offline and never downloads model/runtime dependencies.
func TestNativeVectorRetrieval(t *testing.T) {
	source := os.Getenv("AKASHA_TEST_MODEL_DATA")
	if source == "" {
		t.Skip("set AKASHA_TEST_MODEL_DATA for real native inference")
	}
	source, err := filepath.Abs(source)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Dir(source), "vector-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	modelDir := embedding.Directory(root)
	if err = os.MkdirAll(modelDir, 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(embedding.Directory(source))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range entries {
		if !f.IsDir() {
			if err = os.Link(filepath.Join(embedding.Directory(source), f.Name()), filepath.Join(modelDir, f.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.Bootstrap("vector-fixture-token-0000000000000000000000"); err != nil {
		t.Fatal(err)
	}
	p, err := m.Authenticate("vector-fixture-token-0000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, space := range []string{"work", "home"} {
		if _, err = m.CreateSpace(p, Space{ID: space, Name: space}); err != nil {
			t.Fatal(err)
		}
	}
	project, err := m.CreateProject(p, Project{ID: "retrieval", SpaceID: "work", Name: "Retrieval"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.Store(p, project.ID, "write")
	if err != nil {
		t.Fatal(err)
	}
	docs := []string{
		"An idempotency key prevents a payment request from charging the customer twice when retries occur.",
		"Password credentials are protected with Argon2id, a salted memory hard derivation function.",
		"A write ahead log lets database readers proceed while a transaction appends changes.",
		"Exponential backoff delays repeated network requests; jitter distributes retries over time.",
		"TLS certificates authenticate the server and encrypt traffic between clients and the service.",
		"An LRU cache evicts the least recently accessed entry when its capacity is reached.",
		"Оптимистическая блокировка сравнивает номер версии и отклоняет устаревшее изменение документа.",
		"Резервная копия позволяет восстановить информацию после повреждения диска.",
		"Dependency injection supplies a component with its collaborators through constructor parameters.",
		"A circuit breaker temporarily stops calls to an unavailable downstream service.",
		"CSS grid controls the placement of visual items in rows and columns.",
		"A garbage collector reclaims memory occupied by objects that are no longer reachable.",
	}
	records := []Record{}
	for i, text := range docs {
		r, _, e := s.Put(ctx, p.ID, WriteInput{Kind: "note", Title: "Technical decision", Text: text, Entity: NewID()})
		if e != nil {
			t.Fatal(i, e)
		}
		records = append(records, r)
	}
	_, _, err = s.Put(ctx, p.ID, WriteInput{Kind: "prompt", Title: "excluded prompt", Text: docs[0]})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Put(ctx, p.ID, WriteInput{Kind: "secret", Title: "excluded credential", Blob: []byte("fixture ciphertext, never a real credential")})
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.IndexVectors(ctx, p, project.ID)
	if err != nil || first.Chunks != len(docs) || first.Embedded != len(docs) {
		t.Fatalf("index %+v %v", first, err)
	}
	second, err := m.IndexVectors(ctx, p, project.ID)
	if err != nil || second.Embedded != 0 {
		t.Fatal("repeat recomputed", second, err)
	}
	queries := []struct {
		Text string
		Want int
	}{
		{"Как избежать повторного списания денег при отправке заказа?", 0},
		{"Защита учётных данных от перебора с дорогими вычислениями", 1},
		{"Пауза между попытками соединения должна увеличиваться", 3},
		{"Безопасная передача информации по сети", 4},
		{"Recover data after storage device failure", 7},
		{"Reject edits made from an outdated document revision", 6},
	}
	type measurement struct {
		Query       string  `json:"query"`
		Rank        int     `json:"rank"`
		LexicalRank int     `json:"lexical_rank"`
		MS          float64 `json:"ms"`
	}
	measured := []measurement{}
	durations := []float64{}
	hits := 0
	lexicalHits := 0
	for _, q := range queries {
		start := time.Now()
		found, e := m.VectorSearch(ctx, p, project.ID, q.Text, false, 5)
		if e != nil {
			t.Fatal(e)
		}
		ms := float64(time.Since(start).Microseconds()) / 1000
		rank := 0
		for i, h := range found {
			if h.ID == records[q.Want].ID {
				rank = i + 1
			}
		}
		lexical, e := s.Search(ctx, q.Text, 5)
		if e != nil {
			t.Fatal(e)
		}
		lr := 0
		for i, h := range lexical {
			if h.ID == records[q.Want].ID {
				lr = i + 1
			}
		}
		if lr > 0 {
			lexicalHits++
		}
		if rank > 0 {
			hits++
		}
		measured = append(measured, measurement{q.Text, rank, lr, ms})
		durations = append(durations, ms)
		packet, e := m.Context(ctx, p, project.ID, ContextRequest{Query: q.Text, Budget: 512})
		if e != nil || packet.Tokens > 512 {
			t.Fatalf("context %+v %v", packet, e)
		}
	}
	if hits != len(queries) {
		t.Errorf("semantic recall@5 %d/%d: %+v", hits, len(queries), measured)
	}
	for _, query := range []string{"unregisteredopaquezzabcd", "MissingPaymentRetry", "missing_function_v29", "src/absent.go"} {
		found, e := m.Search(ctx, p, project.ID, query, false, 5)
		if e != nil || len(found) != 0 {
			t.Fatalf("unknown identifier %q matched unrelated semantic evidence: %+v %v", query, found, e)
		}
		packet, e := m.Context(ctx, p, project.ID, ContextRequest{Query: query, Budget: 512})
		if e != nil || len(packet.Selected) != 0 || packet.Retrieval != "lexical-identifier" {
			t.Fatalf("unknown identifier context %q: %+v %v", query, packet, e)
		}
	}
	reader, _, err := m.CreateUser(p, "isolated")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.VectorSearch(ctx, reader, project.ID, queries[0].Text, false, 5); !errors.Is(err, ErrDenied) {
		t.Fatal("ACL", err)
	}
	old := records[0]
	_, _, err = s.Put(ctx, p.ID, WriteInput{Entity: old.Entity, Kind: "note", Title: old.Title, Text: "A new unrelated version about keyboard shortcuts.", Expected: []string{old.ID}})
	if err != nil {
		t.Fatal(err)
	}
	found, err := m.VectorSearch(ctx, p, project.ID, queries[0].Text, false, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range found {
		if h.ID == old.ID {
			t.Fatal("obsolete vector returned")
		}
	}
	changed, err := m.IndexVectors(ctx, p, project.ID)
	if err != nil || changed.Embedded != 1 || changed.Reused != len(docs)-1 {
		t.Fatal("incremental", changed, err)
	}
	sort.Float64s(durations)
	report := map[string]any{"model": embedding.ModelID, "queries": measured, "recall_at_5": float64(hits) / float64(len(queries)), "lexical_recall_at_5": float64(lexicalHits) / float64(len(queries)), "first_index": first, "repeat_index": second, "incremental_index": changed, "query_p95_ms": durations[len(durations)-1], "offline": true, "scope_denied": true, "stale_heads_excluded": true}
	b, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(b))
	if path := os.Getenv("AKASHA_VECTOR_REPORT"); path != "" {
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
