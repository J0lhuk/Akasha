package ilang

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"akasha/internal/core"
)

func fixture(t testing.TB) Service {
	t.Helper()
	m, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	token := core.NewID() + core.NewID()
	if err = m.Bootstrap(token); err != nil {
		t.Fatal(err)
	}
	actor, err := m.Authenticate(token)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Register(actor, core.Space{ID: "work", Name: "Work"}, core.Project{ID: "demo", SpaceID: "work", Name: "Demo", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return Service{m, actor, "demo"}
}
func enabled(t testing.TB) Service {
	t.Helper()
	s := fixture(t)
	if err := s.Configure(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	return s
}
func atom(name string) Definition {
	return Definition{Format: Format, Name: name, Description: "Example reusable server recipe", Parameters: []Parameter{{Name: "port", Type: "integer", Question: "На каком порту запустить сервер?"}}, Prompt: "Create server on port {{port}}. Verify health.", Checks: []string{"GET /healthz returns 200"}, Effects: []string{"project.write"}}
}
func define(t testing.TB, s Service, d Definition) Version {
	t.Helper()
	v, err := s.Define(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func ref(v Version) Ref { return Ref{v.Definition.Name, v.Version} }
func composition(name string, versions ...Version) Definition {
	d := Definition{Format: Format, Name: name, Description: "Compose reusable procedures", Parameters: []Parameter{{Name: "port", Type: "integer", Question: "Какой порт?"}}, Checks: []string{"Integration smoke passes"}, Effects: []string{"project.write"}}
	for i, v := range versions {
		id := string(rune('a' + i))
		step := Step{ID: id, Skill: ref(v), Arguments: map[string]Binding{"port": {Input: "port"}}}
		if i > 0 {
			step.DependsOn = []string{string(rune('a' + i - 1))}
		}
		d.Steps = append(d.Steps, step)
	}
	return d
}
func start(t testing.TB, s Service, r Request) SavedSession {
	t.Helper()
	v, err := s.Start(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func answer(t testing.TB, s Service, r string, a Answers) SavedSession {
	t.Helper()
	v, err := s.Answer(context.Background(), r, a)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func fullReport(s SavedSession) Report {
	r := Report{PlanHash: s.Session.Result.PlanHash, Analysis: "Fixture executor checked each acceptance criterion."}
	for _, p := range s.Session.Result.Plan {
		o := StepOutcome{ID: p.ID, Status: "passed"}
		for _, c := range p.Checks {
			o.Checks = append(o.Checks, CheckOutcome{c, true, "fixture://check-output"})
		}
		r.Steps = append(r.Steps, o)
	}
	return r
}
func TestQuestionsBeforePlanAndOnePassSubstitution(t *testing.T) {
	s := enabled(t)
	d := atom("server")
	d.Parameters = append(d.Parameters, Parameter{Name: "label", Type: "string", Question: "Название?"})
	d.Prompt = "{{label}} {{port}}"
	define(t, s, d)
	r := start(t, s, Request{Skill: Ref{Name: "server"}, Goal: "Создай server", Arguments: map[string]string{"port": "unclear"}, Issues: []Issue{{ID: "hosting", Question: "Где размещаем?"}}})
	if len(r.Session.Result.Questions) != 3 || r.Session.Result.MasterPrompt != "" || len(r.Session.Result.Plan) != 0 {
		t.Fatal("incomplete requirements produced a plan", r)
	}
	r = answer(t, s, r.Revision, Answers{Values: map[string]string{"parameters.port": "8080", "parameters.label": "literal {{port}}"}})
	if r.Session.Result.Status != "needs_input" || r.Session.Result.MasterPrompt != "" {
		t.Fatal("unresolved issue was ignored")
	}
	r = answer(t, s, r.Revision, Answers{Values: map[string]string{"issues.hosting": "Локально в Docker"}})
	if r.Session.Result.Status != "ready" || r.Session.Result.Plan[0].Prompt != `"literal {{port}}" "8080"` || r.Session.Result.Tokens > r.Session.Request.TokenBudget {
		t.Fatal("wrong compilation", r)
	}
	paused := answer(t, s, r.Revision, Answers{NewIssues: []Issue{{ID: "os", Question: "Уточнить ОС"}}})
	if paused.Session.Result.MasterPrompt != "" || len(paused.Session.Result.Plan) != 0 {
		t.Fatal("new issue retained executable plan")
	}
	if _, err := s.Answer(context.Background(), r.Revision, Answers{Values: map[string]string{"parameters.port": "9090"}}); !errors.Is(err, core.ErrConflict) {
		t.Fatal("stale answer overwrote a concurrent question", err)
	}
}
func TestCompositionPinsNestedOrderAndVersions(t *testing.T) {
	s := enabled(t)
	a := define(t, s, atom("server"))
	b := define(t, s, atom("auth"))
	inner := composition("service", a, b)
	inner.Prompt = " \n\t" // A blank optional field must not suppress subskills.
	inner.Steps[0], inner.Steps[1] = inner.Steps[1], inner.Steps[0]
	v := define(t, s, inner)
	outer := define(t, s, composition("video_service", v))
	changed := atom("server")
	changed.Prompt = "New implementation {{port}}"
	define(t, s, changed)
	ambiguous := start(t, s, Request{Skill: Ref{Name: "server"}, Goal: "Create server"})
	if len(ambiguous.Session.Result.Questions) != 1 || len(ambiguous.Session.Result.Questions[0].Choices) != 2 {
		t.Fatal("silently selected latest version")
	}
	r := start(t, s, Request{Skill: ref(outer), Goal: "Create service", Arguments: map[string]string{"port": "8080"}})
	p := r.Session.Result.Plan
	if len(p) != 4 || p[0].Skill.Version != a.Version || p[1].Skill.Version != b.Version || p[2].Kind != "integration_check" || p[3].ID != "video_service._verify" {
		t.Fatal("wrong nested plan", p)
	}
	repeat := start(t, s, r.Session.Request)
	if repeat.Session.Result.PlanHash != r.Session.Result.PlanHash {
		t.Fatal("deterministic compilation changed")
	}
}
func TestInvalidDefinitions(t *testing.T) {
	s := enabled(t)
	child := define(t, s, atom("server"))
	cases := map[string]func(*Definition){
		"cycle":               func(d *Definition) { d.Steps[0].DependsOn = []string{"a"} },
		"unknown-dependency":  func(d *Definition) { d.Steps[0].DependsOn = []string{"missing"} },
		"missing-binding":     func(d *Definition) { d.Steps[0].Arguments = nil },
		"unknown-input":       func(d *Definition) { d.Steps[0].Arguments["port"] = Binding{Input: "absent"} },
		"type-mismatch":       func(d *Definition) { d.Parameters[0].Type = "boolean" },
		"bad-literal":         func(d *Definition) { v := "no"; d.Steps[0].Arguments["port"] = Binding{Literal: &v} },
		"ambiguous-binding":   func(d *Definition) { v := "8000"; d.Steps[0].Arguments["port"] = Binding{Literal: &v, Input: "port"} },
		"unlisted-effect":     func(d *Definition) { d.Effects = nil },
		"wrong-version-name":  func(d *Definition) { d.Steps[0].Skill.Name = "other" },
		"unpinned":            func(d *Definition) { d.Steps[0].Skill.Version = "" },
		"two-implementations": func(d *Definition) { d.Prompt = "unexpected" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := composition("candidate", child)
			mutate(&d)
			if _, err := s.Define(context.Background(), d); err == nil {
				t.Fatal("invalid contract accepted")
			}
		})
	}
	d := atom("candidate")
	d.Prompt = "{{undeclared}}"
	if _, err := s.Define(context.Background(), d); err == nil {
		t.Fatal("undeclared slot accepted")
	}
	if _, err := Decode[Definition]([]byte(`{"format":"akasha.ilang/v1","defaults":{"port":"8080"}}`)); err == nil {
		t.Fatal("unknown defaults accepted")
	}
	if _, err := Decode[Definition]([]byte(`{} {}`)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
func TestLimitsAndCredentials(t *testing.T) {
	s := enabled(t)
	d := atom("server")
	d.Prompt = strings.Repeat("Long precise instruction. ", 2000) + " {{port}}"
	v := define(t, s, d)
	r := start(t, s, Request{Skill: ref(v), Goal: "Create server", Arguments: map[string]string{"port": "8080"}, TokenBudget: 512})
	if r.Session.Result.Status != "needs_input" || r.Session.Result.Questions[0].Key != "token_budget" || r.Session.Result.MasterPrompt != "" {
		t.Fatal("over-budget prompt emitted")
	}
	if _, err := s.Start(context.Background(), Request{Skill: ref(v), Goal: "task", TokenBudget: 32001}); err == nil {
		t.Fatal("unbounded budget")
	}
	d = atom("leaky")
	d.Prompt = "password=credential-that-must-not-be-stored"
	if _, err := s.Define(context.Background(), d); err == nil {
		t.Fatal("credential accepted")
	}
	v = define(t, s, atom("leaf"))
	for i := 0; i < 15; i++ {
		v = define(t, s, composition("layer"+string(rune('a'+i)), v))
	}
	if _, err := s.Define(context.Background(), composition("too_deep", v)); err == nil {
		t.Fatal("unbounded nested expansion")
	}
}
func TestAnalysisAndReusableCapture(t *testing.T) {
	s := enabled(t)
	v := define(t, s, atom("server"))
	r := start(t, s, Request{Skill: ref(v), Goal: "Create server", Arguments: map[string]string{"port": "8080"}})
	if _, err := s.Capture(context.Background(), r.Revision, "premature"); err == nil {
		t.Fatal("captured unexecuted plan")
	}
	report := fullReport(r)
	report.Steps[0].Checks = nil
	if _, err := s.Assess(context.Background(), r.Revision, report); err == nil {
		t.Fatal("passed without checks")
	}
	report = fullReport(r)
	report.PlanHash = "wrong"
	if _, err := s.Assess(context.Background(), r.Revision, report); err == nil {
		t.Fatal("wrong plan report accepted")
	}
	report = fullReport(r)
	report.Unresolved = []string{"Load testing is pending"}
	failed, err := s.Assess(context.Background(), r.Revision, report)
	if err != nil || failed.Session.Result.Status != "reported_incomplete" {
		t.Fatal(err, failed)
	}
	if _, err = s.Capture(context.Background(), failed.Revision, "unfinished"); err == nil {
		t.Fatal("captured incomplete result")
	}
	r = start(t, s, r.Session.Request)
	done, err := s.Assess(context.Background(), r.Revision, fullReport(r))
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.Capture(context.Background(), done.Revision, "server_vide_h")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Definition.Evidence) != 1 || v.Definition.Parameters[0].Name != "port" {
		t.Fatal("lost recipe provenance/parameters")
	}
	next := start(t, s, Request{Skill: ref(v), Goal: "Create another server_vide_h"})
	if next.Session.Result.Status != "needs_input" {
		t.Fatal("last run values became implicit defaults")
	}
}

func TestCapturePreservesQuestionsAndVaultReferences(t *testing.T) {
	s := enabled(t)
	d := atom("server")
	d.Parameters = append(d.Parameters, Parameter{Name: "password", Type: "secret_ref", Question: "Какой доступ из vault использовать?"})
	d.Prompt += " Credentials reference: {{password}}."
	v := define(t, s, d)
	r := start(t, s, Request{Skill: ref(v), Goal: "Create", Arguments: map[string]string{"port": "8080", "password": "vault://project-access"}, Issues: []Issue{{ID: "hosting", Question: "Где разместить?", Answer: "Docker locally"}}})
	done, err := s.Assess(context.Background(), r.Revision, fullReport(r))
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.Capture(context.Background(), done.Revision, "server_vide_h")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Definition.Parameters) != 3 || v.Definition.Parameters[2].Question != "Где разместить?" {
		t.Fatal("capture dropped conversational conditions")
	}
	next := start(t, s, Request{Skill: ref(v), Goal: "Create another"})
	if len(next.Session.Result.Questions) != 3 {
		t.Fatal("capture failed to ask all parameters")
	}
	old, err := s.Read(context.Background(), r.Revision)
	if err != nil || old.Current {
		t.Fatal("old ready plan shown as current", err)
	}
}
func TestIsolationACLAndRetrieval(t *testing.T) {
	ctx := context.Background()
	s := fixture(t)
	if on, err := s.Enabled(ctx); err != nil || on {
		t.Fatal("enabled by default", err)
	}
	if _, err := s.Define(ctx, atom("server")); !errors.Is(err, ErrDisabled) {
		t.Fatal("disabled mode accepted work", err)
	}
	if err := s.Configure(ctx, true); err != nil {
		t.Fatal(err)
	}
	v := define(t, s, atom("server"))
	start(t, s, Request{Skill: ref(v), Goal: "Server"})
	db, err := s.Manager.Store(s.Actor, s.Project, "read")
	if err != nil {
		t.Fatal(err)
	}
	if rows, e := db.Knowledge(ctx, 100); e != nil || len(rows) != 0 {
		t.Fatal("instructions leaked into evidence", e, rows)
	}
	if rev, e := db.KnowledgeRevision(ctx); e != nil || rev != 0 {
		t.Fatal("skills changed retrieval revision", e, rev)
	}
	u, _, err := s.Manager.CreateUser(s.Actor, "Reader")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Manager.Grant(s.Actor, core.Grant{UserID: u.ID, ProjectID: s.Project, Role: "reader"}); err != nil {
		t.Fatal(err)
	}
	reader := Service{s.Manager, u, s.Project}
	if _, err = reader.Catalog(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Define(ctx, atom("bad")); !errors.Is(err, core.ErrDenied) {
		t.Fatal("reader wrote skill", err)
	}
	if err = reader.Configure(ctx, false); !errors.Is(err, core.ErrDenied) {
		t.Fatal("reader changed feature flag", err)
	}
	_, err = s.Manager.Register(s.Actor, core.Space{ID: "home", Name: "Home"}, core.Project{ID: "private", SpaceID: "home", Name: "Private", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	other := Service{s.Manager, s.Actor, "private"}
	if err = other.Configure(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err = other.Start(ctx, Request{Skill: ref(v), Goal: "Cross group"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("foreign project version resolved", err)
	}
	if err = s.Configure(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Catalog(ctx, ""); !errors.Is(err, ErrDisabled) {
		t.Fatal("disabled existing service retained access", err)
	}
}
func TestCheckpointCarriesSkillsButNotActivation(t *testing.T) {
	ctx := context.Background()
	s := enabled(t)
	v := define(t, s, atom("server"))
	run := start(t, s, Request{Skill: ref(v), Goal: "Create"})
	db, _ := s.Manager.Store(s.Actor, s.Project, "write")
	path, err := db.Checkpoint(ctx, "ilang fixture")
	if err != nil {
		t.Fatal(err)
	}
	dst := fixture(t)
	target, _ := dst.Manager.Store(dst.Actor, dst.Project, "write")
	if _, err = target.RestoreCheckpoint(ctx, path); err != nil {
		t.Fatal(err)
	}
	if on, err := dst.Enabled(ctx); err != nil || on {
		t.Fatal("restore auto-enabled ilang", err)
	}
	if err = dst.Configure(ctx, true); err != nil {
		t.Fatal(err)
	}
	restored, err := dst.Read(ctx, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := json.Marshal(run.Session)
	b2, _ := json.Marshal(restored.Session)
	if string(b1) != string(b2) {
		t.Fatal("lost run context")
	}
	answered := answer(t, dst, run.Revision, Answers{Values: map[string]string{"parameters.port": "9090"}})
	if answered.Session.Result.Status != "ready" {
		t.Fatal("restored composition not usable")
	}
}

func TestRevisionIntegrityAndSymbolIdentity(t *testing.T) {
	s := enabled(t)
	v := define(t, s, atom("server"))
	db, _ := s.Manager.Store(s.Actor, s.Project, "read")
	r, err := db.Get(context.Background(), v.Version)
	if err != nil {
		t.Fatal(err)
	}
	r.Title = "another_symbol"
	if _, err = fromRecord(r); err == nil {
		t.Fatal("title redirected symbol lookup to another definition")
	}
	r.Title = "server"
	r.Blob = append(r.Blob, ' ')
	if _, err = fromRecord(r); err == nil {
		t.Fatal("modified skill payload bypassed hash")
	}
}
func BenchmarkCompileComposition(b *testing.B) {
	s := enabled(b)
	leaf := define(b, s, atom("server"))
	v := define(b, s, composition("service", leaf, leaf, leaf))
	db, _ := s.Manager.Store(s.Actor, s.Project, "read")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := Request{Skill: ref(v), Goal: "Create service", Arguments: map[string]string{"port": "8080"}, TokenBudget: 8000}
		result, err := compile(context.Background(), db, &req)
		if err != nil || result.Status != "ready" {
			b.Fatal(err)
		}
	}
}
