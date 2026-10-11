package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"akasha/internal/core"
	"github.com/pelletier/go-toml/v2"
)

type AddResult struct {
	Status       string             `json:"status"`
	Project      *core.Project      `json:"project,omitempty"`
	Spaces       []core.Space       `json:"spaces,omitempty"`
	Created      bool               `json:"created"`
	Import       *core.ImportReport `json:"import,omitempty"`
	ChangedFiles []string           `json:"changed_files,omitempty"`
	Backups      []string           `json:"backups,omitempty"`
	Activation   string             `json:"activation,omitempty"`
}

// Add may resume a partially completed import. Catalog identity is committed
// before importing, so an interrupted retry finds the same registered project.
func Add(ctx context.Context, m *core.Manager, p core.Principal, root, space, name, executable, data string) (AddResult, error) {
	result := AddResult{Status: "ready"}
	root, err := core.CanonicalDirectory(root)
	if err != nil {
		return result, err
	}
	project, err := m.ProjectAt(p, root)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return result, err
	}
	if err == nil {
		if space != "" && project.SpaceID != space {
			return result, errors.New("project already belongs to another group")
		}
		if err = m.Authorize(p, project.ID, "admin"); err != nil {
			return result, err
		}
		root = project.Root
	} else {
		if !p.Admin {
			return result, core.ErrDenied
		}
		spaces, e := m.Spaces(p)
		if e != nil {
			return result, e
		}
		if space == "" {
			for _, candidate := range spaces {
				if candidate.Root != "" && core.Within(candidate.Root, root) {
					space = candidate.ID
					break
				}
			}
			if space == "" {
				result.Status = "needs_space"
				result.Spaces = spaces
				return result, nil
			}
		}
		chosen, e := m.Space(p, space)
		if e != nil {
			return result, e
		}
		if chosen.Root != "" && !core.Within(chosen.Root, root) {
			return result, errors.New("project is outside the selected group root")
		}
		// Unrooted groups are explicit choices, but cannot capture a folder that
		// is already reserved by a different rooted group.
		for _, candidate := range spaces {
			if candidate.ID != space && candidate.Root != "" && (core.Within(candidate.Root, root) || core.Within(root, candidate.Root)) {
				return result, errors.New("project overlaps another group's root")
			}
		}
		if name == "" {
			name = filepath.Base(root)
		}
		project = core.Project{ID: core.NewID(), SpaceID: space, Name: name, Root: root}
		result.Created = true
	}
	result.Project = &project
	plan, err := prepareAttachment(root, executable, data, project.ID)
	if err != nil {
		return result, err
	}
	if result.Created {
		project, err = m.CreateProject(p, project)
		if err != nil {
			return result, err
		}
		result.Project = &project
	}
	s, err := m.Store(p, project.ID, "write")
	if err != nil {
		return result, err
	}
	// Import before creating integration files: generated scripts are not code knowledge.
	report, err := s.Import(ctx, p.ID)
	result.Import = &report
	if err != nil {
		return result, err
	}
	if _, err = s.ExportWiki(ctx); err != nil {
		return result, err
	}
	changed, backups, err := applyAttachment(plan)
	result.ChangedFiles, result.Backups = changed, backups
	if err != nil {
		return result, err
	}
	result.Activation = "CLI/skill ready now. Reload Codex project MCP and review/trust project hooks in /hooks; hook activation has not been asserted."
	return result, nil
}

type fileChange struct {
	path          string
	before, after []byte
	existed       bool
	mode          os.FileMode
}

func readConfig(path string) ([]byte, bool, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err == nil && len(b) > 1<<20 {
		return nil, true, errors.New("integration file exceeds 1 MiB")
	}
	return b, true, err
}

func prepareAttachment(root, executable, data, project string) ([]fileChange, error) {
	base, err := core.SafePath(root, ".akasha/codex")
	if err != nil {
		return nil, err
	}
	files, err := packageFiles(base, executable, data, project)
	if err != nil {
		return nil, err
	}
	plan := []fileChange{}
	// All reads and validation precede mutations; owned runtime files are
	// immutable unless identical. Modified scripts must be reviewed explicitly.
	for relative, content := range files {
		path, e := core.SafePath(root, filepath.ToSlash(filepath.Join(".akasha/codex", relative)))
		if e != nil {
			return nil, e
		}
		old, existed, e := readConfig(path)
		if e != nil {
			return nil, e
		}
		if existed && !bytes.Equal(old, content) {
			return nil, fmt.Errorf("integration file differs; preserve and review: %s", path)
		}
		mode := os.FileMode(0600)
		if strings.HasSuffix(relative, ".sh") {
			mode = 0700
		}
		plan = append(plan, fileChange{path, old, content, existed, mode})
	}
	configPath, err := core.SafePath(root, ".codex/config.toml")
	if err != nil {
		return nil, err
	}
	before, exists, err := readConfig(configPath)
	if err != nil {
		return nil, err
	}
	var config map[string]any
	if err = toml.Unmarshal(bytes.TrimPrefix(before, []byte{0xef, 0xbb, 0xbf}), &config); err != nil {
		return nil, fmt.Errorf("Codex config: %w", err)
	}
	var wanted map[string]any
	if err = toml.Unmarshal(files["codex-config.fragment.toml"], &wanted); err != nil {
		return nil, err
	}
	servers, ok := config["mcp_servers"].(map[string]any)
	if _, present := config["mcp_servers"]; present && !ok {
		return nil, errors.New("invalid mcp_servers table")
	}
	after := before
	if current, present := servers["akasha"]; present {
		expected := wanted["mcp_servers"].(map[string]any)["akasha"]
		if !reflect.DeepEqual(current, expected) {
			return nil, errors.New("mcp_servers.akasha already has another definition; kept unchanged")
		}
	} else {
		after = append(bytes.Clone(before), []byte("\n# Akasha project memory\n")...)
		after = append(after, files["codex-config.fragment.toml"]...)
		if err = toml.Unmarshal(bytes.TrimPrefix(after, []byte{0xef, 0xbb, 0xbf}), &config); err != nil {
			return nil, err
		}
	}
	plan = append(plan, fileChange{configPath, before, after, exists, 0600})
	hookPath, err := core.SafePath(root, ".codex/hooks.json")
	if err != nil {
		return nil, err
	}
	before, exists, err = readConfig(hookPath)
	if err != nil {
		return nil, err
	}
	current := map[string]any{}
	if exists {
		if err = json.Unmarshal(bytes.TrimPrefix(before, []byte{0xef, 0xbb, 0xbf}), &current); err != nil || current == nil {
			return nil, errors.New("invalid existing hooks.json; kept unchanged")
		}
	}
	var generated map[string]any
	if err = json.Unmarshal(files["hooks/hooks.json"], &generated); err != nil {
		return nil, err
	}
	existingHooks, ok := current["hooks"].(map[string]any)
	if _, present := current["hooks"]; present && !ok {
		return nil, errors.New("invalid existing hooks table")
	}
	if existingHooks == nil {
		existingHooks = map[string]any{}
		current["hooks"] = existingHooks
	}
	changed := false
	for event, raw := range generated["hooks"].(map[string]any) {
		entries, ok := existingHooks[event].([]any)
		if _, present := existingHooks[event]; present && !ok {
			return nil, errors.New("invalid hook event entries")
		}
		addition := raw.([]any)[0]
		found := false
		for _, entry := range entries {
			if reflect.DeepEqual(entry, addition) {
				found = true
				break
			}
			// A modified definition pointing at our runtime is not a foreign hook.
			b, e := json.Marshal(entry)
			if e != nil {
				return nil, e
			}
			if bytes.Contains(b, []byte("Akasha project memory")) || bytes.Contains(b, []byte(".akasha")) {
				return nil, errors.New("existing Akasha hook differs; kept unchanged")
			}
		}
		if !found {
			existingHooks[event] = append(entries, addition)
			changed = true
		}
	}
	after = before
	if changed {
		after, err = json.MarshalIndent(current, "", "  ")
		if err != nil {
			return nil, err
		}
		after = append(after, '\n')
	}
	plan = append(plan, fileChange{hookPath, before, after, exists, 0600})
	sort.Slice(plan, func(i, j int) bool { return plan[i].path < plan[j].path })
	return plan, nil
}

func applyAttachment(plan []fileChange) ([]string, []string, error) {
	changed, backups := []string{}, []string{}
	for _, change := range plan {
		current, exists, err := readConfig(change.path)
		if err != nil {
			return changed, backups, err
		}
		if exists != change.existed || !bytes.Equal(current, change.before) {
			return changed, backups, errors.New("integration changed during setup; retry after review")
		}
	}
	for _, change := range plan {
		if change.existed && bytes.Equal(change.before, change.after) {
			continue
		}
		if change.existed {
			backup := change.path + ".akasha-backup-" + core.Hash(string(change.before))[:16]
			if err := writeNewOrIdentical(backup, change.before, 0600); err != nil {
				return changed, backups, err
			}
			backups = append(backups, backup)
			if err := core.AtomicWrite(change.path, change.after, change.mode); err != nil {
				return changed, backups, err
			}
		} else if err := writeNewOrIdentical(change.path, change.after, change.mode); err != nil {
			return changed, backups, err
		}
		changed = append(changed, change.path)
	}
	return changed, backups, nil
}

func writeNewOrIdentical(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if os.IsExist(err) {
		old, _, e := readConfig(path)
		if e != nil {
			return e
		}
		if bytes.Equal(old, content) {
			return nil
		}
		return errors.New("refusing to replace existing file: " + path)
	}
	if err != nil {
		return err
	}
	_, err = f.Write(content)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
