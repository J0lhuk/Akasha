// akasha-eval exercises the shipping engine on a public deterministic fixture.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"akasha/internal/core"
	"akasha/internal/embedding"
)

type question struct{ ID, Title, Query, Body, Expected string }
type distribution struct {
	Samples int     `json:"samples"`
	P50     float64 `json:"p50_ms"`
	P95     float64 `json:"p95_ms"`
	Max     float64 `json:"max_ms"`
}
type outcome struct {
	ID        string  `json:"id"`
	Query     string  `json:"query"`
	Rank      int     `json:"rank_at_5"`
	Kept      bool    `json:"expected_text_retained"`
	Tokens    int     `json:"context_tokens"`
	Baseline  int     `json:"naive_top5_original_tokens"`
	Reduction float64 `json:"reduction_fraction"`
}
type report struct {
	Created            string                  `json:"created"`
	Go                 string                  `json:"go"`
	OS                 string                  `json:"os"`
	CPUs               int                     `json:"logical_cpus"`
	FixtureHash        string                  `json:"fixture_sha256"`
	Data               string                  `json:"data_directory"`
	Records            int                     `json:"records"`
	Queries            int                     `json:"queries"`
	Budget             int                     `json:"context_budget"`
	Recall             float64                 `json:"recall_at_5"`
	Retention          float64                 `json:"key_text_retention"`
	Reduction          float64                 `json:"aggregate_reduction_vs_top5_originals"`
	BudgetPass         bool                    `json:"all_within_budget"`
	CachePass          bool                    `json:"all_repeats_hit_cache"`
	NoEvidence         bool                    `json:"unmatched_query_has_no_sources"`
	Isolation          bool                    `json:"foreign_group_absent"`
	TokenizerStart     float64                 `json:"tokenizer_first_count_ms"`
	GraphStart         float64                 `json:"graph_first_build_ms"`
	SeedMS             float64                 `json:"seed_ms"`
	SearchFirst        distribution            `json:"fts_first_pass"`
	SearchWarm         distribution            `json:"fts_repeat_passes"`
	ContextMiss        distribution            `json:"context_cache_miss"`
	ContextHit         distribution            `json:"context_cache_hit"`
	ImportFirst        core.ImportReport       `json:"import_first"`
	ImportRepeat       core.ImportReport       `json:"import_repeat"`
	ImportFirstMS      float64                 `json:"import_first_ms"`
	ImportRepeatMS     float64                 `json:"import_repeat_ms"`
	CheckpointRestored int                     `json:"checkpoint_restored_events"`
	CheckpointExpected int                     `json:"checkpoint_expected_events"`
	Cases              []outcome               `json:"cases"`
	Pass               bool                    `json:"acceptance_pass"`
	Retrieval          string                  `json:"retrieval"`
	VectorIndex        *core.VectorIndexReport `json:"vector_index,omitempty"`
}

func must[T any](v T, e error) T {
	if e != nil {
		panic(e)
	}
	return v
}
func check(e error) {
	if e != nil {
		panic(e)
	}
}
func ms(t time.Time) float64 { return float64(time.Since(t).Nanoseconds()) / 1e6 }
func stats(values []float64) distribution {
	sort.Float64s(values)
	at := func(q float64) float64 { return values[int(math.Ceil(q*float64(len(values))))-1] }
	return distribution{len(values), at(.5), at(.95), values[len(values)-1]}
}
func main() {
	fixture := flag.String("fixture", "testdata/retrieval.json", "public fixture path")
	output := flag.String("out", "reports/evaluation.json", "report path")
	modelData := flag.String("model-data", "", "use an already installed local model for the unchanged fixture")
	flag.Parse()
	raw := must(os.ReadFile(*fixture))
	var qs []question
	check(json.Unmarshal(raw, &qs))
	if len(qs) != 20 {
		panic("fixture must contain 20 questions")
	}
	tempBase := ""
	if *modelData != "" {
		*modelData = must(filepath.Abs(*modelData))
		tempBase = filepath.Dir(*modelData)
	}
	dir := must(os.MkdirTemp(tempBase, "akasha-eval-"))
	ctx := context.Background()
	m := must(core.Open(filepath.Join(dir, "memory")))
	if *modelData != "" {
		check(os.MkdirAll(embedding.Directory(m.Root), 0700))
		for _, f := range must(os.ReadDir(embedding.Directory(*modelData))) {
			if !f.IsDir() {
				check(os.Link(filepath.Join(embedding.Directory(*modelData), f.Name()), filepath.Join(embedding.Directory(m.Root), f.Name())))
			}
		}
	}
	defer m.Close()
	token := core.NewID() + core.NewID()
	check(m.Bootstrap(token))
	p := must(m.Authenticate(token))
	_, e := m.CreateSpace(p, core.Space{ID: "work", Name: "Evaluation"})
	check(e)
	_, e = m.CreateProject(p, core.Project{ID: "atlas", SpaceID: "work", Name: "Technical corpus"})
	check(e)
	s := must(m.Store(p, "atlas", "write"))
	r := report{Created: core.Now(), Go: runtime.Version(), OS: runtime.GOOS + "/" + runtime.GOARCH, CPUs: runtime.NumCPU(), FixtureHash: core.Hash(string(raw)), Data: dir, Records: 1000, Queries: len(qs), Budget: 1024, BudgetPass: true, CachePass: true, Cases: []outcome{}}
	start := time.Now()
	must(core.Tokens("Tokenizer startup: русские и English записи"))
	r.TokenizerStart = ms(start)
	start = time.Now()
	for _, q := range qs {
		var body strings.Builder
		body.WriteString(q.Body + "\n\n## Historical verification log\n")
		for i := 0; i < 90; i++ {
			fmt.Fprintf(&body, "Revision %03d: compatibility review for module %s, fixture run %03d; archived checks preserved, no change to the confirmed policy above.\n", i, q.ID, 700+i)
		}
		_, _, e = s.Put(ctx, p.ID, core.WriteInput{Entity: q.ID, Kind: "decision", Title: q.Title, Text: body.String(), Source: "testdata/retrieval.json#" + q.ID})
		check(e)
	}
	topics := []string{"queue latency metrics", "database migration changelog", "API request validation", "контроль доступа команды", "обновление каталога товаров", "ошибки загрузки файлов", "worker deployment status", "backup and restore notes", "cache statistics", "ревизии базы знаний"}
	for i := 0; i < 980; i++ {
		topic := topics[i%len(topics)]
		_, _, e = s.Put(ctx, p.ID, core.WriteInput{Entity: fmt.Sprintf("archive/%04d", i), Kind: "note", Title: fmt.Sprintf("Archive %04d %s", i, topic), Text: fmt.Sprintf("Archived observation %04d about %s. The measurement belongs to a separate test component. Reviewed on fixture day %d; an earlier experiment has no authority over current project decisions.\nСправочная запись: новые настройки требуют отдельного решения и подтверждения исходным кодом.", i, topic, i%28+1), Source: "synthetic distractor"})
		check(e)
	}
	r.SeedMS = ms(start)
	r.Retrieval = "lexical+graph"
	if *modelData != "" {
		v := must(m.IndexVectors(ctx, p, "atlas"))
		r.VectorIndex = &v
		r.Retrieval = "hybrid-local-e5+graph"
	}
	_, e = m.CreateSpace(p, core.Space{ID: "home", Name: "Isolated"})
	check(e)
	_, e = m.CreateProject(p, core.Project{ID: "private", SpaceID: "home", Name: "Private"})
	check(e)
	other := must(m.Store(p, "private", "write"))
	_, _, e = other.Put(ctx, p.ID, core.WriteInput{Entity: "hidden", Kind: "note", Title: "Foreign", Text: "ForeignCanaryExcludedFromWork"})
	check(e)
	r.Isolation = len(must(m.Search(ctx, p, "atlas", "ForeignCanaryExcludedFromWork", true, 5))) == 0
	start = time.Now()
	must(s.Graph(ctx))
	r.GraphStart = ms(start)
	var sf, sw, cm, ch []float64
	found, kept, sumBase, sumContext := 0, 0, 0, 0
	for _, q := range qs {
		start = time.Now()
		hits := must(m.Search(ctx, p, "atlas", q.Query, false, 5))
		sf = append(sf, ms(start))
		rank := 0
		var baseline strings.Builder
		for i, h := range hits {
			if h.Entity == q.ID {
				rank = i + 1
			}
			rec := must(s.Get(ctx, h.ID))
			baseline.WriteString(rec.Title + "\n" + rec.Text + "\n")
		}
		if rank > 0 {
			found++
		}
		req := core.ContextRequest{Query: q.Query, Budget: r.Budget, Phase: "implement"}
		start = time.Now()
		packet := must(m.Context(ctx, p, "atlas", req))
		cm = append(cm, ms(start))
		preserved := strings.Contains(packet.Text, q.Expected)
		if preserved {
			kept++
		}
		if packet.Tokens > r.Budget {
			r.BudgetPass = false
		}
		base := must(core.Tokens(baseline.String()))
		sumBase += base
		sumContext += packet.Tokens
		r.Cases = append(r.Cases, outcome{q.ID, q.Query, rank, preserved, packet.Tokens, base, 1 - float64(packet.Tokens)/float64(max(1, base))})
		for range 5 {
			start = time.Now()
			must(m.Search(ctx, p, "atlas", q.Query, false, 5))
			sw = append(sw, ms(start))
			start = time.Now()
			cached := must(m.Context(ctx, p, "atlas", req))
			ch = append(ch, ms(start))
			r.CachePass = r.CachePass && cached.CacheHit && cached.Text == packet.Text
		}
	}
	r.Recall = float64(found) / float64(len(qs))
	r.Retention = float64(kept) / float64(len(qs))
	r.Reduction = 1 - float64(sumContext)/float64(sumBase)
	r.SearchFirst = stats(sf)
	r.SearchWarm = stats(sw)
	r.ContextMiss = stats(cm)
	r.ContextHit = stats(ch)
	no := must(m.Context(ctx, p, "atlas", core.ContextRequest{Query: "noevidenceopaquezzqrst", Budget: 128}))
	r.NoEvidence = len(no.Selected) == 0
	cp := must(s.Checkpoint(ctx, "evaluation"))
	r.CheckpointExpected = int(must(s.Revision(ctx)))
	restored := must(core.Open(filepath.Join(dir, "restored")))
	defer restored.Close()
	check(restored.Bootstrap(token))
	rp := must(restored.Authenticate(token))
	_, e = restored.Register(rp, core.Space{ID: "work", Name: "Evaluation"}, core.Project{ID: "atlas", SpaceID: "work", Name: "Technical corpus"})
	check(e)
	rs := must(restored.Store(rp, "atlas", "write"))
	r.CheckpointRestored = must(rs.RestoreCheckpoint(ctx, cp))
	root := filepath.Join(dir, "source")
	check(os.MkdirAll(root, 0700))
	// With --model-data the fixture lives inside .local of the repository.
	// Give it its own Git boundary so parent ignore rules cannot hide all files.
	check(exec.Command("git", "init", "--quiet", root).Run())
	for i := 0; i < 100; i++ {
		check(os.WriteFile(filepath.Join(root, fmt.Sprintf("module%03d.go", i)), []byte(fmt.Sprintf("package fixture\n// Calc%d is an isolated test symbol.\nfunc Calc%d(v int) int { return v + %d }\n", i, i, i)), 0600))
	}
	_, e = m.CreateProject(p, core.Project{ID: "source", SpaceID: "work", Name: "Import fixture", Root: root})
	check(e)
	imp := must(m.Store(p, "source", "write"))
	start = time.Now()
	r.ImportFirst = must(imp.Import(ctx, p.ID))
	r.ImportFirstMS = ms(start)
	start = time.Now()
	r.ImportRepeat = must(imp.Import(ctx, p.ID))
	r.ImportRepeatMS = ms(start)
	r.Pass = r.Recall >= .9 && r.Retention == 1 && r.Reduction >= .5 && r.BudgetPass && r.CachePass && r.NoEvidence && r.Isolation && r.SearchFirst.P95 < 100 && r.ContextMiss.P95 < 250 && r.ImportFirst.Files == 100 && r.ImportRepeat.Files == 100 && r.ImportRepeat.Unchanged == r.ImportFirst.Changed && r.ImportRepeat.Changed == 0 && r.CheckpointRestored == r.CheckpointExpected
	reportBytes := must(json.MarshalIndent(r, "", "  "))
	check(core.AtomicWrite(*output, append(reportBytes, '\n'), 0600))
	fmt.Println(string(reportBytes))
	if !r.Pass {
		os.Exit(1)
	}
}
