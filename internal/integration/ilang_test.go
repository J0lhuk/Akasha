package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"akasha/internal/core"
	"akasha/internal/ilang"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestIlangMCPOptInRoundTripAndRevocation(t *testing.T) {
	a := testAccess(t)
	svc := ilang.Service{Manager: a.Manager, Actor: a.Principal, Project: a.Project}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := svc.Configure(ctx, true); err != nil {
		t.Fatal(err)
	}
	server := NewMCP(a)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "ilang-fixture", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	listed, err := cs.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 16 {
		t.Fatal("opt-in tools", err, listed)
	}
	call := func(name string, args any, out any) *mcp.CallToolResult {
		t.Helper()
		r, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if e != nil || r.IsError {
			t.Fatalf("%s: %+v %v", name, r, e)
		}
		b, e := json.Marshal(r.StructuredContent)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(b, out); e != nil {
			t.Fatal(e, string(b))
		}
		return r
	}
	d := ilang.Definition{Format: ilang.Format, Name: "server_vide_h", Description: "Recorded server recipe", Parameters: []ilang.Parameter{{Name: "port", Type: "integer", Question: "Какой порт?"}}, Prompt: "Create server {{port}}", Checks: []string{"health responds"}, Effects: []string{"project.write"}}
	var v ilang.Version
	call("ilang_define", d, &v)
	var r ilangRunMeta
	questionReply := call("ilang_prepare", ilang.Request{Skill: ilang.Ref{Name: d.Name}, Goal: "Создай server_vide_h"}, &r)
	if r.Status != "needs_input" || strings.Contains(questionReply.Content[0].(*mcp.TextContent).Text, "Create server") {
		t.Fatal("missing answers did not stop planning")
	}
	readyReply := call("ilang_answer", ilangAnswer{Revision: r.Revision, Answers: ilang.Answers{Values: map[string]string{"parameters.port": "8080"}}}, &r)
	if r.Status != "ready" {
		t.Fatal("answers did not produce a plan")
	}
	text := readyReply.Content[0].(*mcp.TextContent).Text
	if strings.Count(text, "Create server") != 1 {
		t.Fatal("MCP repeated the skill procedure")
	}
	structured, _ := json.Marshal(readyReply.StructuredContent)
	if strings.Contains(string(structured), "Create server") {
		t.Fatal("MCP duplicated full procedures in metadata")
	}
	n, e := core.Tokens(text)
	if e != nil || n != r.Tokens {
		t.Fatal("MCP text differs from budgeted master prompt", e, n, r.Tokens)
	}
	report := ilang.Report{PlanHash: r.PlanHash, Analysis: "Fixture observed the declared health criterion.", Steps: []ilang.StepOutcome{{ID: "server_vide_h", Status: "passed", Checks: []ilang.CheckOutcome{{Check: "health responds", Passed: true, Evidence: "fixture://health-output"}}}}}
	call("ilang_report", ilangReport{r.Revision, report}, &r)
	call("ilang_capture", ilangCapture{Revision: r.Revision, Name: "recorded_server"}, &v)
	if v.Definition.Name != "recorded_server" || len(v.Definition.Parameters) != 1 {
		t.Fatal("capture lost the callable recipe")
	}
	if err = svc.Configure(ctx, false); err != nil {
		t.Fatal(err)
	}
	denied, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ilang_catalog", Arguments: map[string]any{}})
	if err != nil || !denied.IsError {
		t.Fatal("connected MCP ignored runtime disable", denied, err)
	}
}
