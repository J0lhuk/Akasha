package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ProjectAt resolves only accessible, explicitly registered roots. It never
// guesses a project from its name or from another group's database.
func (m *Manager) ProjectAt(p Principal, cwd string) (Project, error) {
	root, err := CanonicalDirectory(cwd)
	if err != nil {
		return Project{}, err
	}
	spaces, err := m.Spaces(p)
	if err != nil {
		return Project{}, err
	}
	for _, space := range spaces {
		projects, e := m.Projects(p, space.ID)
		if e != nil {
			return Project{}, e
		}
		for _, project := range projects {
			if !project.Shared && project.Root != "" && Within(project.Root, root) {
				return project, nil
			}
		}
	}
	return Project{}, ErrNotFound
}

func CanonicalDirectory(path string) (string, error) {
	if path == "" {
		path = "."
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("project root must be a directory")
	}
	return path, nil
}

// BudgetPolicy is a versioned, syncable project setting, never retrieval evidence.
type BudgetPolicy struct {
	Budgets  map[string]int `json:"budgets"`
	Revision string         `json:"revision,omitempty"`
}

func (s *Store) Budgets(ctx context.Context) (BudgetPolicy, error) {
	p := BudgetPolicy{Budgets: map[string]int{}}
	for phase, budget := range phaseBudget {
		p.Budgets[phase] = budget
	}
	heads, err := s.Heads(ctx, "settings/context-budget")
	if err != nil {
		return p, err
	}
	if len(heads) > 1 {
		return p, fmt.Errorf("context budget policy: %w", ErrConflict)
	}
	if len(heads) == 0 || heads[0].Deleted {
		return p, nil
	}
	if heads[0].Kind != "context_policy" {
		return p, errors.New("invalid context policy kind")
	}
	var saved map[string]int
	if err = json.Unmarshal([]byte(heads[0].Text), &saved); err != nil {
		return p, err
	}
	for phase, budget := range saved {
		if _, ok := phaseBudget[phase]; !ok || budget < 64 || budget > 32000 {
			return p, errors.New("invalid saved context budget")
		}
		p.Budgets[phase] = budget
	}
	p.Revision = heads[0].ID
	return p, nil
}

func (s *Store) SetBudget(ctx context.Context, author, phase string, budget int) (BudgetPolicy, error) {
	if _, ok := phaseBudget[phase]; !ok || budget < 64 || budget > 32000 {
		return BudgetPolicy{}, errors.New("known phase and budget 64..32000 required")
	}
	p, err := s.Budgets(ctx)
	if err != nil {
		return p, err
	}
	p.Budgets[phase] = budget
	b, err := json.Marshal(p.Budgets)
	if err != nil {
		return p, err
	}
	heads, err := s.Heads(ctx, "settings/context-budget")
	if err != nil {
		return p, err
	}
	// Keep the observed revision from Budgets; do not silently overwrite a
	// concurrently changed policy between reading it and issuing the update.
	var expected []string
	if p.Revision != "" {
		expected = []string{p.Revision}
	} else if len(heads) != 0 {
		return p, ErrConflict
	}
	r, _, err := s.Put(ctx, author, WriteInput{Entity: "settings/context-budget", Kind: "context_policy", Title: "Context budgets", Text: string(b), Expected: expected})
	if err != nil {
		return p, err
	}
	p.Revision = r.ID
	return p, nil
}

var templateName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)
var templateVariable = regexp.MustCompile(`\{\{([a-zA-Z][a-zA-Z0-9_]{0,63})\}\}`)

func (s *Store) SavePrompt(ctx context.Context, author, name, text string, expected []string) (Record, bool, error) {
	if !templateName.MatchString(name) || strings.TrimSpace(text) == "" || len(text) > 128<<10 {
		return Record{}, false, errors.New("prompt requires name (letters/digits/_/-) and text up to 128 KiB")
	}
	return s.Put(ctx, author, WriteInput{Entity: "templates/" + name, Kind: "prompt_template", Title: name, Text: text, Expected: expected})
}

func (s *Store) Prompt(ctx context.Context, name string) (Record, error) {
	if !templateName.MatchString(name) {
		return Record{}, errors.New("invalid prompt name")
	}
	heads, err := s.Heads(ctx, "templates/"+name)
	if err != nil {
		return Record{}, err
	}
	if len(heads) > 1 {
		return Record{}, ErrConflict
	}
	if len(heads) == 0 || heads[0].Deleted {
		return Record{}, ErrNotFound
	}
	if heads[0].Kind != "prompt_template" {
		return Record{}, errors.New("invalid template kind")
	}
	return heads[0], nil
}

type RenderedPrompt struct {
	Text      string `json:"text"`
	Tokens    int    `json:"tokens"`
	Tokenizer string `json:"tokenizer"`
	Revision  string `json:"revision"`
	CacheHit  bool   `json:"cache_hit"`
}

func (s *Store) RenderPrompt(ctx context.Context, name string, variables map[string]string) (RenderedPrompt, error) {
	r, err := s.Prompt(ctx, name)
	if err != nil {
		return RenderedPrompt{}, err
	}
	if len(variables) > 64 {
		return RenderedPrompt{}, errors.New("at most 64 template variables allowed")
	}
	clean := map[string]string{}
	for key, value := range variables {
		if len(key) > 64 || len(value) > 128<<10 {
			return RenderedPrompt{}, errors.New("template variable exceeds limits")
		}
		clean[key] = Redact(value)
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		return RenderedPrompt{}, err
	}
	key := "prompt:" + Hash(r.ID+string(encoded))
	if raw, ok, e := s.cacheGet(ctx, key, 0); e != nil {
		return RenderedPrompt{}, e
	} else if ok {
		var rendered RenderedPrompt
		if e = json.Unmarshal([]byte(raw), &rendered); e != nil {
			return rendered, e
		}
		rendered.CacheHit = true
		return rendered, nil
	}
	missing := ""
	text := templateVariable.ReplaceAllStringFunc(r.Text, func(match string) string {
		name := match[2 : len(match)-2]
		value, ok := clean[name]
		if !ok {
			missing = name
		}
		return value
	})
	if missing != "" {
		return RenderedPrompt{}, fmt.Errorf("missing prompt variable: %s", missing)
	}
	if len(text) > 2<<20 {
		return RenderedPrompt{}, errors.New("rendered prompt exceeds 2 MiB")
	}
	text = Redact(text)
	n, err := Tokens(text)
	if err != nil {
		return RenderedPrompt{}, err
	}
	out := RenderedPrompt{Text: text, Tokens: n, Tokenizer: "o200k_base", Revision: r.ID}
	b, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if err = s.cachePut(ctx, key, 0, string(b)); err != nil {
		return out, err
	}
	return out, nil
}

type CacheStats struct {
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
}

func (s *Store) CacheStats(ctx context.Context) (CacheStats, error) {
	var result CacheStats
	err := s.db.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(length(CAST(value AS BLOB))),0) FROM cache").Scan(&result.Entries, &result.Bytes)
	return result, err
}
func (s *Store) ClearCache(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, "DELETE FROM cache")
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
