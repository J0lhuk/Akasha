package integration

import (
	"context"
	"encoding/json"
	"time"

	"akasha/internal/ilang"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ilangName struct {
	Name string `json:"name,omitempty"`
}
type ilangVersions struct {
	Versions []ilang.Version `json:"versions"`
}
type ilangRevision struct {
	Revision string `json:"revision"`
}
type ilangAnswer struct {
	Revision string        `json:"revision"`
	Answers  ilang.Answers `json:"answers"`
}
type ilangReport struct {
	Revision string       `json:"revision"`
	Report   ilang.Report `json:"report"`
}
type ilangCapture struct {
	Revision string `json:"revision"`
	Name     string `json:"name"`
}

type ilangRunMeta struct {
	Revision         string `json:"revision"`
	RunID            string `json:"run_id"`
	Current          bool   `json:"current"`
	Status           string `json:"status"`
	PlanHash         string `json:"plan_hash,omitempty"`
	Tokens           int    `json:"master_prompt_tokens,omitempty"`
	Tokenizer        string `json:"tokenizer,omitempty"`
	ExternalAPICalls int    `json:"external_api_calls"`
}

// Send procedure text once. Returning SavedSession as structured output would
// duplicate every prompt in both Plan and MasterPrompt (and again as MCP text).
func ilangReply(saved ilang.SavedSession, err error) (*mcp.CallToolResult, ilangRunMeta, error) {
	if err != nil {
		return nil, ilangRunMeta{}, err
	}
	s := saved.Session
	meta := ilangRunMeta{Revision: saved.Revision, RunID: s.ID, Current: saved.Current, Status: s.Result.Status, PlanHash: s.Result.PlanHash, Tokens: s.Result.Tokens, Tokenizer: s.Result.Tokenizer}
	text := s.Result.MasterPrompt
	if s.Result.Status == "needs_input" {
		b, e := json.MarshalIndent(struct {
			Questions []ilang.Question  `json:"questions"`
			Known     map[string]string `json:"known_parameters,omitempty"`
		}{s.Result.Questions, s.Request.Arguments}, "", "  ")
		if e != nil {
			return nil, meta, e
		}
		text = string(b)
	} else if s.Report != nil {
		b, e := json.MarshalIndent(s.Report, "", "  ")
		if e != nil {
			return nil, meta, e
		}
		text = string(b)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, meta, nil
}

func addIlang(server *mcp.Server, a Access) {
	s := ilang.Service{Manager: a.Manager, Actor: a.Principal, Project: a.Project}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	enabled, err := s.Enabled(ctx)
	cancel()
	if err != nil || !enabled {
		return
	}
	no := false
	tool := func(name, description string, read bool) *mcp.Tool {
		return &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: read, DestructiveHint: &no, OpenWorldHint: &no}}
	}
	mcp.AddTool(server, tool("ilang_catalog", "Read named skills and immutable versions in this project. Never choose among multiple versions silently.", true), func(ctx context.Context, _ *mcp.CallToolRequest, in ilangName) (*mcp.CallToolResult, ilangVersions, error) {
		out, e := s.Catalog(ctx, in.Name)
		return nil, ilangVersions{out}, e
	})
	mcp.AddTool(server, tool("ilang_define", "Save an explicitly requested reusable skill or composition. Include parameter questions, pinned subskills, dependencies and result checks. Never execute the definition here.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in ilang.Definition) (*mcp.CallToolResult, ilang.Version, error) {
		out, e := s.Define(ctx, in)
		return nil, out, e
	})
	mcp.AddTool(server, tool("ilang_prepare", "Prepare an explicit skill invocation. Pass ALL known ambiguities as issues. Ask every returned question; needs_input has no master prompt or executable plan. ready returns a pinned plan for the authorized Codex task.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in ilang.Request) (*mcp.CallToolResult, ilangRunMeta, error) {
		out, e := s.Start(ctx, in)
		return ilangReply(out, e)
	})
	mcp.AddTool(server, tool("ilang_answer", "Apply explicit answers to the observed current revision. New issues pause a ready plan. Do not invent missing answers or treat a known word in quoted material as invocation.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in ilangAnswer) (*mcp.CallToolResult, ilangRunMeta, error) {
		out, e := s.Answer(ctx, in.Revision, in.Answers)
		return ilangReply(out, e)
	})
	mcp.AddTool(server, tool("ilang_read", "Read a specific saved invocation revision for continuation and its questions, plan or report.", true), func(ctx context.Context, _ *mcp.CallToolRequest, in ilangRevision) (*mcp.CallToolResult, ilangRunMeta, error) {
		out, e := s.Read(ctx, in.Revision)
		return ilangReply(out, e)
	})
	mcp.AddTool(server, tool("ilang_report", "Record actual execution evidence for every step and criterion, plus analysis against the goal. reported_complete means executor-reported evidence coverage, not an independently run test. Include failures and unresolved questions.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in ilangReport) (*mcp.CallToolResult, ilangRunMeta, error) {
		out, e := s.Assess(ctx, in.Revision, in.Report)
		return ilangReply(out, e)
	})
	mcp.AddTool(server, tool("ilang_capture", "On the user's request, name a completed run's reusable recipe as a new skill. Keeps all parameters open and pins subskills; does not install globally or execute it.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in ilangCapture) (*mcp.CallToolResult, ilang.Version, error) {
		out, e := s.Capture(ctx, in.Revision, in.Name)
		return nil, out, e
	})
}
