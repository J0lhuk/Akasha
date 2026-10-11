package integration

import (
	"context"
	"errors"
	"strings"

	"akasha/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Access struct {
	Manager   *core.Manager
	Principal core.Principal
	Project   string
}
type searchInput struct {
	Query  string `json:"query"`
	Limit  int    `json:"limit,omitempty"`
	Shared bool   `json:"shared,omitempty"`
}
type readInput struct {
	ID      string `json:"id"`
	Project string `json:"project_id,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	Budget  int    `json:"budget,omitempty"`
}
type readOutput struct {
	ID         string `json:"id"`
	Entity     string `json:"entity"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	Tokens     int    `json:"tokens"`
	NextOffset int    `json:"next_offset"`
	Truncated  bool   `json:"truncated"`
}
type emptyInput struct{}
type hitsOutput struct {
	Hits []core.Hit `json:"hits"`
}
type conflictsOutput struct {
	Conflicts  []core.Conflict `json:"conflicts"`
	NextOffset int             `json:"next_offset"`
	Total      int             `json:"total"`
}
type pageInput struct {
	Offset int `json:"offset,omitempty"`
	Limit  int `json:"limit,omitempty"`
}
type writeInput struct {
	Entity    string      `json:"entity"`
	Kind      string      `json:"kind"`
	Title     string      `json:"title"`
	Text      string      `json:"text,omitempty"`
	Source    string      `json:"source,omitempty"`
	Expected  []string    `json:"expected,omitempty"`
	Subject   string      `json:"subject,omitempty"`
	Predicate string      `json:"predicate,omitempty"`
	Value     string      `json:"value,omitempty"`
	ValidFrom string      `json:"valid_from,omitempty"`
	ValidTo   string      `json:"valid_to,omitempty"`
	Links     []core.Link `json:"links,omitempty"`
	Tags      []string    `json:"tags,omitempty"`
}
type receipt struct {
	ID      string `json:"id"`
	Entity  string `json:"entity"`
	Created string `json:"created"`
}

func saved(r core.Record) receipt { return receipt{r.ID, r.Entity, r.Created} }
func memoryKind(kind string) bool { return kind != "secret" && kind != "file" && kind != "git_bundle" }

type contextMeta struct {
	Tokens    int    `json:"tokens"`
	Budget    int    `json:"budget"`
	Tokenizer string `json:"tokenizer"`
	Omitted   int    `json:"omitted"`
	CacheHit  bool   `json:"cache_hit"`
}
type resolveInput struct {
	Entity   string   `json:"entity"`
	Chosen   string   `json:"chosen"`
	Reason   string   `json:"reason"`
	Expected []string `json:"expected"`
}
type historyInput struct {
	Entity string `json:"entity"`
}
type historyOutput struct {
	Versions []core.Hit `json:"versions"`
}
type pathOutput struct {
	Path string `json:"path"`
}

func (a Access) Read(ctx context.Context, in readInput) (readOutput, error) {
	out := readOutput{}
	project := a.Project
	if in.Project != "" && in.Project != project {
		scopes, e := a.Manager.Scopes(a.Principal, a.Project, true)
		if e != nil {
			return out, e
		}
		allowed := false
		for _, s := range scopes {
			if s.Project.ID == in.Project {
				allowed = true
			}
		}
		if !allowed {
			return out, core.ErrDenied
		}
		project = in.Project
	}
	s, err := a.Manager.Store(a.Principal, project, "read")
	if err != nil {
		return out, err
	}
	r, err := s.Get(ctx, in.ID)
	if err != nil {
		return out, err
	}
	if r.Kind == "secret" || r.Kind == "file" || r.Kind == "git_bundle" {
		return out, core.ErrDenied
	}
	if in.Budget == 0 {
		in.Budget = 1800
	}
	if in.Budget < 32 || in.Budget > 8000 || in.Offset < 0 {
		return out, errors.New("read budget 32..8000 and nonnegative offset required")
	}
	text := r.Text
	if r.Kind == "fact" {
		text = r.Subject + " / " + r.Predicate + " = " + r.Value + "\n" + text
	}
	runes := []rune(text)
	if in.Offset > len(runes) {
		return out, errors.New("offset exceeds content length")
	}
	remaining := runes[in.Offset:]
	lo, hi := 0, len(remaining)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		n, e := core.Tokens(string(remaining[:mid]))
		if e != nil {
			return out, e
		}
		if n <= in.Budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	out = readOutput{ID: r.ID, Entity: r.Entity, Title: r.Title, Text: string(remaining[:lo]), NextOffset: in.Offset + lo, Truncated: lo < len(remaining)}
	out.Tokens, err = core.Tokens(out.Text)
	return out, err
}
func NewMCP(a Access) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "akasha", Version: "0.2.0"}, nil)
	no := false
	tool := func(name, description string, read bool) *mcp.Tool {
		return &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: read, DestructiveHint: &no, OpenWorldHint: &no}}
	}
	mcp.AddTool(server, tool("memory_context", "Rank local evidence into a hard text token budget. Start here; citations link to originals. Protocol metadata is outside the text budget.", true), func(ctx context.Context, _ *mcp.CallToolRequest, in core.ContextRequest) (*mcp.CallToolResult, contextMeta, error) {
		out, e := a.Manager.Context(ctx, a.Principal, a.Project, in)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out.Text}}}, contextMeta{out.Tokens, out.Budget, out.Tokenizer, out.Omitted, out.CacheHit}, e
	})
	mcp.AddTool(server, tool("memory_search", "Search this project and optionally its readable group memory. Returns short citations.", true), func(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, hitsOutput, error) {
		out, e := a.Manager.Search(ctx, a.Principal, a.Project, in.Query, in.Shared, in.Limit)
		return nil, hitsOutput{out}, e
	})
	mcp.AddTool(server, tool("memory_read", "Read an original revision in bounded chunks. offset counts Unicode characters. Secrets are excluded.", true), func(ctx context.Context, _ *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, readOutput, error) {
		out, e := a.Read(ctx, in)
		return nil, out, e
	})
	mcp.AddTool(server, tool("memory_write", "Persist evidence, decisions or structured facts. Updating requires expected current revision ids. Conflicting facts are retained.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in writeInput) (*mcp.CallToolResult, receipt, error) {
		switch in.Kind {
		case "note", "decision", "task", "fact", "tool":
		default:
			return nil, receipt{}, errors.New("unsupported memory kind")
		}
		s, e := a.Manager.Store(a.Principal, a.Project, "write")
		if e != nil {
			return nil, receipt{}, e
		}
		r, _, e := s.Put(ctx, a.Principal.ID, core.WriteInput{Entity: in.Entity, Kind: in.Kind, Title: in.Title, Text: in.Text, Source: in.Source, Expected: in.Expected, Subject: in.Subject, Predicate: in.Predicate, Value: in.Value, ValidFrom: in.ValidFrom, ValidTo: in.ValidTo, Links: in.Links, Tags: in.Tags})
		return nil, saved(r), e
	})
	mcp.AddTool(server, tool("memory_conflicts", "Page unresolved knowledge alternatives. Default 5, maximum 10. Read original revisions before resolving.", true), func(ctx context.Context, _ *mcp.CallToolRequest, in pageInput) (*mcp.CallToolResult, conflictsOutput, error) {
		if in.Offset < 0 || in.Limit < 0 || in.Limit > 10 {
			return nil, conflictsOutput{}, errors.New("nonnegative offset and limit <=10 required")
		}
		if in.Limit == 0 {
			in.Limit = 5
		}
		s, e := a.Manager.Store(a.Principal, a.Project, "read")
		if e != nil {
			return nil, conflictsOutput{}, e
		}
		cs, e := s.Conflicts(ctx)
		filtered := cs[:0]
		for _, c := range cs {
			if len(c.Versions) > 0 && memoryKind(c.Versions[0].Kind) {
				filtered = append(filtered, c)
			}
		}
		total := len(filtered)
		start := min(in.Offset, total)
		end := min(start+in.Limit, total)
		cs = filtered[start:end]
		for i := range cs {
			for j := range cs[i].Versions {
				r := cs[i].Versions[j]
				short := func(s string) string {
					rr := []rune(s)
					if len(rr) > 240 {
						return string(rr[:240]) + "…"
					}
					return s
				}
				cs[i].Versions[j] = core.Record{ID: r.ID, Entity: r.Entity, Kind: r.Kind, Title: short(r.Title), Text: short(r.Text), Subject: short(r.Subject), Predicate: short(r.Predicate), Value: short(r.Value), Created: r.Created, Deleted: r.Deleted}
			}
		}
		return nil, conflictsOutput{cs, end, total}, e
	})
	mcp.AddTool(server, tool("memory_resolve", "Resolve an explicitly confirmed alternative with a reason and every current head id; history is kept.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in resolveInput) (*mcp.CallToolResult, receipt, error) {
		s, e := a.Manager.Store(a.Principal, a.Project, "write")
		if e != nil {
			return nil, receipt{}, e
		}
		chosen, e := s.Get(ctx, in.Chosen)
		if e != nil {
			return nil, receipt{}, e
		}
		if !memoryKind(chosen.Kind) {
			return nil, receipt{}, core.ErrDenied
		}
		r, e := s.Resolve(ctx, a.Principal.ID, in.Entity, in.Chosen, in.Reason, in.Expected)
		return nil, saved(r), e
	})
	mcp.AddTool(server, tool("memory_checkpoint", "Create a consistent local checkpoint before compaction or handoff.", false), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, pathOutput, error) {
		s, e := a.Manager.Store(a.Principal, a.Project, "write")
		if e != nil {
			return nil, pathOutput{}, e
		}
		path, e := s.Checkpoint(ctx, "MCP")
		return nil, pathOutput{path}, e
	})
	mcp.AddTool(server, tool("memory_history", "List prior revisions without expanding their contents.", true), func(ctx context.Context, _ *mcp.CallToolRequest, in historyInput) (*mcp.CallToolResult, historyOutput, error) {
		s, e := a.Manager.Store(a.Principal, a.Project, "read")
		if e != nil {
			return nil, historyOutput{}, e
		}
		rs, e := s.History(ctx, in.Entity)
		out := historyOutput{Versions: []core.Hit{}}
		for _, r := range rs {
			out.Versions = append(out.Versions, core.Hit{ID: r.ID, Entity: r.Entity, ProjectID: a.Project, Kind: r.Kind, Title: r.Title, Snippet: r.Created})
			if len(out.Versions) >= 100 {
				break
			}
		}
		return nil, out, e
	})
	mcp.AddTool(server, tool("memory_import", "Incrementally index the configured source root without executing project code.", false), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, core.ImportReport, error) {
		s, e := a.Manager.Store(a.Principal, a.Project, "write")
		if e != nil {
			return nil, core.ImportReport{}, e
		}
		out, e := s.Import(ctx, a.Principal.ID)
		return nil, out, e
	})
	addIlang(server, a)
	return server
}
func citationText(s string) string { return strings.TrimSpace(core.Redact(s)) }
