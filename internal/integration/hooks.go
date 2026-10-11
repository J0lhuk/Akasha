package integration

import (
	"context"
	"errors"
	"path/filepath"

	"akasha/internal/core"
)

type HookInput struct {
	Event       string `json:"hook_event_name"`
	Session     string `json:"session_id"`
	Turn        string `json:"turn_id"`
	CWD         string `json:"cwd"`
	Prompt      string `json:"prompt"`
	LastMessage string `json:"last_assistant_message"`
	Source      string `json:"source"`
	StopActive  bool   `json:"stop_hook_active"`
}
type HookSpecific struct {
	Event   string `json:"hookEventName"`
	Context string `json:"additionalContext"`
}
type HookOutput struct {
	Continue bool          `json:"continue"`
	Specific *HookSpecific `json:"hookSpecificOutput,omitempty"`
}

func (a Access) Hook(ctx context.Context, in HookInput, budget int) (HookOutput, error) {
	out := HookOutput{Continue: true}
	s, err := a.Manager.Store(a.Principal, a.Project, "write")
	if err != nil {
		return out, err
	}
	cwd, err := filepath.EvalSymlinks(in.CWD)
	if err != nil || s.Project.Root == "" || !core.Within(s.Project.Root, cwd) {
		return out, errors.New("hook cwd is outside the bound project")
	}
	if len(in.Session) > 256 || len(in.Turn) > 256 {
		return out, errors.New("invalid hook identifiers")
	}
	switch in.Event {
	case "SessionStart", "UserPromptSubmit":
		if in.Event == "UserPromptSubmit" && in.Prompt != "" {
			entity := "prompts/" + core.Hash(in.Session+"/"+in.Turn+"/"+in.Prompt)
			_, _, err = s.Put(ctx, a.Principal.ID, core.WriteInput{Entity: entity, Kind: "prompt", Title: "User prompt", Text: core.Redact(in.Prompt), Meta: map[string]string{"session": in.Session, "turn": in.Turn}})
			if err != nil {
				return out, err
			}
		}
		phase := "resume"
		if in.Event == "UserPromptSubmit" {
			phase = "implement"
		}
		packet, e := a.Manager.Context(ctx, a.Principal, a.Project, core.ContextRequest{Query: in.Prompt, Budget: budget, Phase: phase, Shared: true})
		if e != nil {
			return out, e
		}
		out.Specific = &HookSpecific{in.Event, packet.Text}
	case "Stop", "PreCompact", "SessionEnd":
		if in.Event == "Stop" && in.LastMessage != "" {
			entity := "turns/" + core.Hash(in.Session+"/"+in.Turn+"/"+in.LastMessage)
			_, _, err = s.Put(ctx, a.Principal.ID, core.WriteInput{Entity: entity, Kind: "checkpoint", Title: "Last completed turn", Text: core.Redact(in.LastMessage), Meta: map[string]string{"session": in.Session, "turn": in.Turn}})
			if err != nil {
				return out, err
			}
		}
		if _, err = s.Checkpoint(ctx, in.Event); err != nil {
			return out, err
		}
		if _, err = s.ExportWiki(ctx); err != nil {
			return out, err
		}
	default:
		return out, errors.New("unsupported hook event")
	}
	return out, nil
}
