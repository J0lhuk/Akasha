// Package ilang composes explicitly invoked, versioned skills. It never runs
// commands or models: the resulting plan is executed by the authorized agent.
package ilang

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"akasha/internal/core"
)

const Format = "akasha.ilang/v1"
const maxDefinition = 128 << 10

var symbol = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)
var slot = regexp.MustCompile(`\{\{([a-zA-Z][a-zA-Z0-9_-]{0,63})\}\}`)
var vaultJSON = regexp.MustCompile(`"[a-zA-Z][a-zA-Z0-9_-]{0,63}"\s*:\s*"vault://[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}"`)
var ErrDisabled = errors.New("ilang is disabled; explicitly enable it for this local project")

type Parameter struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Question string   `json:"question"`
	Choices  []string `json:"choices,omitempty"`
}
type Ref struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}
type Binding struct {
	Literal *string `json:"literal,omitempty"`
	Input   string  `json:"input,omitempty"`
}
type Step struct {
	ID        string             `json:"id"`
	Skill     Ref                `json:"skill"`
	Arguments map[string]Binding `json:"arguments,omitempty"`
	DependsOn []string           `json:"depends_on,omitempty"`
}
type Definition struct {
	Format      string      `json:"format"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  []Parameter `json:"parameters,omitempty"`
	Prompt      string      `json:"prompt,omitempty"`
	Steps       []Step      `json:"steps,omitempty"`
	Checks      []string    `json:"checks"`
	Effects     []string    `json:"effects,omitempty"`
	Evidence    []string    `json:"evidence,omitempty"`
}
type Version struct {
	Version    string     `json:"version"`
	SHA256     string     `json:"sha256"`
	Definition Definition `json:"definition"`
}
type Issue struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Answer   string `json:"answer,omitempty"`
}
type Request struct {
	Skill       Ref               `json:"skill"`
	Goal        string            `json:"goal"`
	Arguments   map[string]string `json:"arguments,omitempty"`
	Issues      []Issue           `json:"issues,omitempty"`
	TokenBudget int               `json:"token_budget,omitempty"`
}
type Question struct {
	Key     string   `json:"key"`
	Text    string   `json:"text"`
	Choices []string `json:"choices,omitempty"`
}
type PlanStep struct {
	ID        string            `json:"id"`
	Skill     Ref               `json:"skill"`
	Kind      string            `json:"kind"`
	Arguments map[string]string `json:"arguments,omitempty"`
	Prompt    string            `json:"prompt,omitempty"`
	Checks    []string          `json:"checks"`
	Effects   []string          `json:"effects,omitempty"`
}
type Result struct {
	Status           string     `json:"status"`
	Questions        []Question `json:"questions,omitempty"`
	Plan             []PlanStep `json:"plan,omitempty"`
	MasterPrompt     string     `json:"master_prompt,omitempty"`
	PlanHash         string     `json:"plan_hash,omitempty"`
	Tokens           int        `json:"tokens,omitempty"`
	Tokenizer        string     `json:"tokenizer,omitempty"`
	ExternalAPICalls int        `json:"external_api_calls"`
}
type Session struct {
	ID      string  `json:"id"`
	Request Request `json:"request"`
	Result  Result  `json:"result"`
	Report  *Report `json:"report,omitempty"`
}
type SavedSession struct {
	Revision string  `json:"revision"`
	Current  bool    `json:"current"`
	Session  Session `json:"session"`
}
type Answers struct {
	Values    map[string]string `json:"values,omitempty"`
	NewIssues []Issue           `json:"new_issues,omitempty"`
}
type CheckOutcome struct {
	Check    string `json:"check"`
	Passed   bool   `json:"passed"`
	Evidence string `json:"evidence"`
}
type StepOutcome struct {
	ID     string         `json:"id"`
	Status string         `json:"status"`
	Checks []CheckOutcome `json:"checks,omitempty"`
}
type Report struct {
	PlanHash   string        `json:"plan_hash"`
	Steps      []StepOutcome `json:"steps"`
	Analysis   string        `json:"analysis"`
	Unresolved []string      `json:"unresolved,omitempty"`
}

// Decode rejects misspelled fields and trailing JSON; inputs contain no code.
func Decode[T any](data []byte) (T, error) {
	var out T
	if len(data) > 2<<20 || !utf8.Valid(data) {
		return out, errors.New("JSON exceeds limit or is not UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return out, err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return out, errors.New("expected exactly one JSON value")
	}
	return out, nil
}
func clean(value any, limit int) ([]byte, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, errors.New("ilang material exceeds size limit")
	}
	// A reference named password is not a password value. Mask only validated
	// vault identifiers for the best-effort credential scan, never stored data.
	scanned := vaultJSON.ReplaceAllString(string(b), `"vault_reference":"reference"`)
	if core.Redact(scanned) != scanned {
		return nil, errors.New("possible credential in ilang material; use a vault reference")
	}
	return b, nil
}
func validValue(p Parameter, value string) bool {
	if strings.TrimSpace(value) == "" || len(value) > 8192 {
		return false
	}
	switch p.Type {
	case "string":
		return true
	case "integer":
		_, err := strconv.ParseInt(value, 10, 64)
		return err == nil
	case "boolean":
		return value == "true" || value == "false"
	case "enum":
		for _, c := range p.Choices {
			if value == c {
				return true
			}
		}
		return false
	case "secret_ref":
		return strings.HasPrefix(value, "vault://") && core.ValidateID(strings.TrimPrefix(value, "vault://")) == nil
	}
	return false
}
func validDefinition(d Definition) error {
	if d.Format != Format || !symbol.MatchString(d.Name) || strings.TrimSpace(d.Description) == "" || len(d.Description) > 2000 {
		return errors.New("skill requires format, symbol name and description")
	}
	if (strings.TrimSpace(d.Prompt) == "") == (len(d.Steps) == 0) {
		return errors.New("define exactly one of prompt or steps")
	}
	if len(d.Parameters) > 32 || len(d.Steps) > 32 || len(d.Checks) == 0 || len(d.Checks) > 32 || len(d.Effects) > 16 || len(d.Evidence) > 32 {
		return errors.New("skill contract exceeds limits or lacks checks")
	}
	params := map[string]bool{}
	for _, p := range d.Parameters {
		if !symbol.MatchString(p.Name) || params[p.Name] || strings.TrimSpace(p.Question) == "" || len(p.Question) > 2000 {
			return errors.New("parameters require unique names and questions")
		}
		params[p.Name] = true
		switch p.Type {
		case "string", "integer", "boolean", "secret_ref":
			if len(p.Choices) != 0 {
				return errors.New("choices only apply to enum")
			}
		case "enum":
			if len(p.Choices) == 0 || len(p.Choices) > 32 {
				return errors.New("enum requires choices")
			}
		default:
			return errors.New("unknown parameter type")
		}
		seen := map[string]bool{}
		for _, c := range p.Choices {
			if strings.TrimSpace(c) == "" || len(c) > 8192 || seen[c] {
				return errors.New("invalid enum choices")
			}
			seen[c] = true
		}
	}
	for _, s := range [][]string{d.Checks, d.Effects, d.Evidence} {
		seen := map[string]bool{}
		for _, v := range s {
			if strings.TrimSpace(v) == "" || len(v) > 4000 || seen[v] {
				return errors.New("empty, duplicate or oversized contract entry")
			}
			seen[v] = true
		}
	}
	for _, effect := range d.Effects {
		switch effect {
		case "project.read", "project.write", "network", "deploy":
		default:
			return errors.New("unknown effect")
		}
	}
	for _, m := range slot.FindAllStringSubmatch(d.Prompt, -1) {
		if !params[m[1]] {
			return errors.New("prompt references an undeclared parameter")
		}
	}
	rest := slot.ReplaceAllString(d.Prompt, "")
	if strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
		return errors.New("invalid prompt placeholder")
	}
	_, err := ordered(d.Steps)
	if err != nil {
		return err
	}
	_, err = clean(d, maxDefinition)
	return err
}

// Stable topological order; input order breaks ties. No implicit parallelism.
func ordered(steps []Step) ([]Step, error) {
	ids := map[string]bool{}
	for _, s := range steps {
		if !symbol.MatchString(s.ID) || ids[s.ID] || !symbol.MatchString(s.Skill.Name) || core.ValidateID(s.Skill.Version) != nil {
			return nil, errors.New("steps require unique ids and pinned skill versions")
		}
		ids[s.ID] = true
		if len(s.DependsOn) > 32 || len(s.Arguments) > 32 {
			return nil, errors.New("step exceeds limits")
		}
		for _, b := range s.Arguments {
			if (b.Literal == nil) == (b.Input == "") {
				return nil, errors.New("binding requires exactly one of literal or input")
			}
		}
	}
	for _, s := range steps {
		seen := map[string]bool{}
		for _, dep := range s.DependsOn {
			if !ids[dep] || seen[dep] {
				return nil, errors.New("unknown or duplicate dependency")
			}
			seen[dep] = true
		}
	}
	done := map[string]bool{}
	out := make([]Step, 0, len(steps))
	for len(out) < len(steps) {
		progress := false
		for _, s := range steps {
			if done[s.ID] {
				continue
			}
			ready := true
			for _, d := range s.DependsOn {
				if !done[d] {
					ready = false
				}
			}
			if ready {
				out = append(out, s)
				done[s.ID] = true
				progress = true
			}
		}
		if !progress {
			return nil, errors.New("cyclic skill dependencies")
		}
	}
	return out, nil
}
