package ilang

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"akasha/internal/core"
)

// Assess checks coverage of the executor's evidence. It does not impersonate
// an external test runner: reported_complete is explicitly an attestation.
func (s Service) Assess(ctx context.Context, revision string, report Report) (SavedSession, error) {
	db, err := s.store(ctx, "write")
	if err != nil {
		return SavedSession{}, err
	}
	session, err := loadSession(ctx, db, revision)
	if err != nil {
		return SavedSession{}, err
	}
	if session.Result.Status != "ready" || len(session.Result.Plan) == 0 {
		return SavedSession{}, errors.New("analysis requires a ready plan")
	}
	if report.PlanHash != session.Result.PlanHash || report.PlanHash != core.Hash(session.Result.MasterPrompt) {
		return SavedSession{}, errors.New("report belongs to a different plan")
	}
	if strings.TrimSpace(report.Analysis) == "" || len(report.Analysis) > 16000 || len(report.Unresolved) > 32 {
		return SavedSession{}, errors.New("bounded result analysis required")
	}
	for _, u := range report.Unresolved {
		if strings.TrimSpace(u) == "" || len(u) > 2000 {
			return SavedSession{}, errors.New("invalid unresolved issue")
		}
	}
	if len(report.Steps) != len(session.Result.Plan) {
		return SavedSession{}, errors.New("report every step, including blocked or skipped steps")
	}
	outcomes := map[string]StepOutcome{}
	for _, step := range report.Steps {
		if _, ok := outcomes[step.ID]; ok {
			return SavedSession{}, errors.New("duplicate step report")
		}
		switch step.Status {
		case "passed", "failed", "blocked", "skipped":
		default:
			return SavedSession{}, errors.New("unknown step result status")
		}
		outcomes[step.ID] = step
	}
	complete := len(report.Unresolved) == 0
	for _, step := range session.Result.Plan {
		r, ok := outcomes[step.ID]
		if !ok {
			return SavedSession{}, errors.New("missing step result")
		}
		seen := map[string]bool{}
		for _, check := range r.Checks {
			if !slices.Contains(step.Checks, check.Check) || seen[check.Check] || strings.TrimSpace(check.Evidence) == "" || len(check.Evidence) > 8000 {
				return SavedSession{}, errors.New("each reported check needs a known unique criterion and evidence")
			}
			seen[check.Check] = true
			if r.Status == "passed" && !check.Passed {
				return SavedSession{}, errors.New("passed step contains a failed check")
			}
		}
		if r.Status == "passed" && len(seen) != len(step.Checks) {
			return SavedSession{}, errors.New("passed step is missing verification evidence")
		}
		if r.Status != "passed" {
			complete = false
		}
	}
	session.Report = &report
	session.Result.Status = "reported_incomplete"
	if complete {
		session.Result.Status = "reported_complete"
	}
	return saveSession(ctx, db, s.Actor.ID, session, []string{revision})
}

// Capture keeps parameters open for the next invocation. Values from this one
// run never become implicit defaults in the reusable function.
func (s Service) Capture(ctx context.Context, revision, name string) (Version, error) {
	db, err := s.store(ctx, "write")
	if err != nil {
		return Version{}, err
	}
	session, err := loadSession(ctx, db, revision)
	if err != nil {
		return Version{}, err
	}
	if session.Result.Status != "reported_complete" || session.Report == nil {
		return Version{}, errors.New("capture requires a completed report with all checks; use define for a separately authored recipe")
	}
	v, err := getVersion(ctx, db, session.Request.Skill)
	if err != nil {
		return Version{}, err
	}
	d := v.Definition
	d.Name = name
	d.Evidence = append(d.Evidence, "ilang-run:"+revision)
	// A conversational clarification can be essential to the method. Preserve
	// the question as a fresh input, never silently bake in that run's answer.
	known := parameters(d)
	n := 1
	for _, issue := range session.Request.Issues {
		key := fmt.Sprintf("clarification_%d", n)
		for {
			if _, exists := known[key]; !exists {
				break
			}
			n++
			key = fmt.Sprintf("clarification_%d", n)
		}
		p := Parameter{Name: key, Type: "string", Question: issue.Question}
		d.Parameters = append(d.Parameters, p)
		known[key] = p
		n++
	}
	return s.Define(ctx, d)
}
