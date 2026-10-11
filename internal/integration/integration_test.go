package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"akasha/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testAccess(t *testing.T) Access {
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
	p, err := m.Authenticate(token)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Register(p, core.Space{ID: "work", Name: "Work"}, core.Project{ID: "demo", SpaceID: "work", Name: "Demo", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return Access{m, p, "demo"}
}
func TestMCPHandshakeListAndCalls(t *testing.T) {
	a := testAccess(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	server := NewMCP(a)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	listed, err := cs.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 9 {
		t.Fatalf("tools: %+v %v", listed, err)
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "memory_write", Arguments: map[string]any{"entity": "database", "kind": "note", "title": "Database", "text": "Orders use PostgreSQL", "source": "confirmed deployment"}})
	if err != nil || result.IsError {
		t.Fatalf("write: %+v %v", result, err)
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "memory_context", Arguments: map[string]any{"query": "Orders PostgreSQL", "budget": 500, "phase": "implement", "shared": true}})
	if err != nil || result.IsError {
		t.Fatalf("context: %+v %v", result, err)
	}
	raw, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(raw), "PostgreSQL") {
		t.Fatalf("missing context: %s %v", raw, err)
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "memory_write", Arguments: map[string]any{"entity": "secret", "kind": "secret", "title": "Password", "text": "bad", "source": "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("secret accepted through general MCP write")
	}
}
func TestHooksContractsScopeAndCheckpoint(t *testing.T) {
	a := testAccess(t)
	ctx := context.Background()
	s, err := a.Manager.Store(a.Principal, a.Project, "write")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Put(ctx, a.Principal.ID, core.WriteInput{Entity: "cache", Kind: "note", Title: "Cache", Text: "Cache expires after 300 seconds"}); err != nil {
		t.Fatal(err)
	}
	out, err := a.Hook(ctx, HookInput{Event: "UserPromptSubmit", Session: "s1", Turn: "t1", CWD: s.Project.Root, Prompt: "Check cache behavior password=not-saved"}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Continue || out.Specific == nil || out.Specific.Event != "UserPromptSubmit" || !strings.Contains(out.Specific.Context, "300 seconds") {
		t.Fatalf("hook output: %+v", out)
	}
	stop, err := a.Hook(ctx, HookInput{Event: "Stop", Session: "s1", Turn: "t1", CWD: s.Project.Root, LastMessage: "Implemented cache fix", StopActive: true}, 500)
	if err != nil || !stop.Continue || stop.Specific != nil {
		t.Fatalf("stop loops or malformed output: %+v %v", stop, err)
	}
	files, err := filepath.Glob(filepath.Join(s.Dir(), "checkpoints", "*.jsonl.gz"))
	if err != nil || len(files) != 1 {
		t.Fatal("checkpoint missing", err)
	}
	if _, err = a.Hook(ctx, HookInput{Event: "SessionStart", CWD: t.TempDir()}, 500); err == nil {
		t.Fatal("foreign hook cwd accepted")
	}
	history, err := s.List(ctx, "prompt", 10)
	if err != nil || len(history) != 1 || strings.Contains(history[0].Text, "not-saved") {
		t.Fatal("prompt redaction failed", err)
	}
	destination := filepath.Join(t.TempDir(), "plugin")
	if err = Package(destination, "C:/program files/akasha/akasha.exe", a.Manager.Root, a.Project); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(destination, "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	if err = Package(destination, "akasha.exe", a.Manager.Root, a.Project); err == nil {
		t.Fatal("existing integration overwritten")
	}
}
