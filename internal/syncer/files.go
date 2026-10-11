package syncer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"akasha/internal/core"
	"akasha/internal/vault"
)

type FileReport struct {
	Saved     int          `json:"saved"`
	Unchanged int          `json:"unchanged"`
	Skipped   []string     `json:"skipped"`
	Actions   []FileAction `json:"actions"`
}
type FileAction struct {
	Path     string `json:"path"`
	Action   string `json:"action"`
	Revision string `json:"revision"`
}

func Capture(ctx context.Context, s *core.Store, author string, key vault.Key) (FileReport, error) {
	report := FileReport{Skipped: []string{}, Actions: []FileAction{}}
	files, err := core.SourceFiles(ctx, s.Project.Root)
	if err != nil {
		return report, err
	}
	seen := map[string]bool{}
	for _, rel := range files {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		entity := "files/" + rel
		seen[entity] = true
		data, e := core.ReadSource(s.Project.Root, rel, 4<<20)
		if e != nil {
			report.Skipped = append(report.Skipped, rel+": "+e.Error())
			continue
		}
		hash := core.Hash(string(data))
		heads, e := s.Heads(ctx, entity)
		if e != nil {
			return report, e
		}
		if len(heads) > 1 {
			report.Skipped = append(report.Skipped, rel+": unresolved versions")
			continue
		}
		if len(heads) == 1 && !heads[0].Deleted && heads[0].Meta["sha256"] == hash {
			report.Unchanged++
			continue
		}
		blob, e := vault.EncryptBlob(key, s.Project, entity, data)
		clear(data)
		if e != nil {
			return report, e
		}
		expected := []string{}
		for _, h := range heads {
			expected = append(expected, h.ID)
		}
		_, changed, e := s.Put(ctx, author, core.WriteInput{Entity: entity, Kind: "file", Title: rel, Source: rel, Blob: blob, Expected: expected, Meta: map[string]string{"sha256": hash, "format": "aes-gcm-v1"}})
		if e != nil {
			return report, e
		}
		if changed {
			report.Saved++
		}
	}
	if len(report.Skipped) == 0 {
		records, e := s.List(ctx, "file", 10000)
		if e != nil {
			return report, e
		}
		if len(records) == 10000 {
			return report, errors.New("file inventory limit reached")
		}
		for _, r := range records {
			if seen[r.Entity] {
				continue
			}
			heads, e := s.Heads(ctx, r.Entity)
			if e != nil {
				return report, e
			}
			if len(heads) != 1 {
				continue
			}
			if _, _, e = s.Put(ctx, author, core.WriteInput{Entity: r.Entity, Kind: r.Kind, Title: r.Title, Source: r.Source, Expected: []string{r.ID}, Deleted: true, Meta: r.Meta}); e != nil {
				return report, e
			}
			report.Saved++
		}
	}
	return report, nil
}
func Restore(ctx context.Context, s *core.Store, key vault.Key, apply bool) (FileReport, error) {
	report := FileReport{Skipped: []string{}, Actions: []FileAction{}}
	root := s.Project.Root
	if root == "" {
		return report, errors.New("set a local project root before restoring files")
	}
	records, err := s.Current(ctx, "file", 10000)
	if err != nil {
		return report, err
	}
	if len(records) == 10000 {
		return report, errors.New("file restore limit reached")
	}
	seen := map[string]bool{}
	for _, r := range records {
		if seen[r.Entity] {
			continue
		}
		seen[r.Entity] = true
		rel := strings.TrimPrefix(r.Entity, "files/")
		if rel == r.Entity || core.SensitivePath(rel) {
			report.Skipped = append(report.Skipped, rel+": invalid or excluded path")
			continue
		}
		dst, e := core.SafePath(root, rel)
		if e != nil {
			return report, e
		}
		heads, e := s.Heads(ctx, r.Entity)
		if e != nil {
			return report, e
		}
		if len(heads) > 1 {
			report.Actions = append(report.Actions, FileAction{rel, "unresolved_versions", r.ID})
			continue
		}
		if r.Deleted {
			report.Actions = append(report.Actions, FileAction{rel, "deletion_pending", r.ID})
			continue
		}
		plain, e := vault.DecryptBlob(key, s.Project, r)
		if e != nil {
			return report, e
		}
		hash := core.Hash(string(plain))
		if hash != r.Meta["sha256"] {
			clear(plain)
			return report, errors.New("file content hash mismatch")
		}
		action := "create"
		existing, e := os.ReadFile(dst)
		if e == nil {
			existingHash := core.Hash(string(existing))
			if existingHash == hash {
				action = "unchanged"
			} else {
				previous, e := s.State(ctx, "restored/"+rel)
				if e != nil {
					clear(plain)
					return report, e
				}
				if previous == existingHash {
					action = "update"
				} else {
					action = "conflict_copy"
					dst, e = core.SafePath(root, ".akasha-conflicts/"+r.ID+"/"+rel)
					if e != nil {
						clear(plain)
						return report, e
					}
				}
			}
		} else if !os.IsNotExist(e) {
			clear(plain)
			return report, e
		}
		report.Actions = append(report.Actions, FileAction{rel, action, r.ID})
		if apply && action != "unchanged" {
			if err = core.AtomicWrite(dst, plain, 0600); err != nil {
				clear(plain)
				return report, err
			}
			report.Saved++
		}
		clear(plain)
		if apply && action != "conflict_copy" {
			if err = s.SetState(ctx, "restored/"+rel, hash); err != nil {
				return report, err
			}
		}
		if action == "unchanged" {
			report.Unchanged++
		}
	}
	return report, nil
}

func CaptureGitBundle(ctx context.Context, s *core.Store, author string, key vault.Key) (core.Record, error) {
	if s.Project.Root == "" {
		return core.Record{}, errors.New("source root required")
	}
	headCmd := exec.CommandContext(ctx, "git", "-C", s.Project.Root, "show-ref")
	refs, err := headCmd.Output()
	if err != nil {
		return core.Record{}, errors.New("Git repository with at least one ref required")
	}
	digest := core.Hash(string(refs))
	heads, err := s.Heads(ctx, "git/bundle")
	if err != nil {
		return core.Record{}, err
	}
	if len(heads) > 1 {
		return core.Record{}, core.ErrConflict
	}
	if len(heads) == 1 && heads[0].Meta["refs_sha256"] == digest {
		return heads[0], nil
	}
	dir, err := os.MkdirTemp("", "akasha-git-")
	if err != nil {
		return core.Record{}, err
	}
	file := filepath.Join(dir, "project.bundle")
	defer os.Remove(dir)
	defer os.Remove(file)
	cmd := exec.CommandContext(ctx, "git", "-C", s.Project.Root, "bundle", "create", file, "--all")
	if err = cmd.Run(); err != nil {
		return core.Record{}, errors.New("Git bundle creation failed")
	}
	data, err := core.ReadSource(dir, "project.bundle", 8<<20)
	if err != nil {
		return core.Record{}, errors.New("Git bundle exceeds 8 MiB or is unreadable; transfer large Git history through its Git remote")
	}
	defer clear(data)
	blob, err := vault.EncryptBlob(key, s.Project, "git/bundle", data)
	if err != nil {
		return core.Record{}, err
	}
	expected := []string{}
	for _, h := range heads {
		expected = append(expected, h.ID)
	}
	r, _, err := s.Put(ctx, author, core.WriteInput{Entity: "git/bundle", Kind: "git_bundle", Title: "Git history", Blob: blob, Expected: expected, Meta: map[string]string{"refs_sha256": digest, "sha256": core.Hash(string(data)), "format": "aes-gcm-v1"}})
	return r, err
}
func ExportGitBundle(ctx context.Context, s *core.Store, key vault.Key, destination string) error {
	records, err := s.List(ctx, "git_bundle", 100)
	if err != nil {
		return err
	}
	if len(records) != 1 {
		return errors.New("exactly one resolved Git bundle required")
	}
	plain, err := vault.DecryptBlob(key, s.Project, records[0])
	if err != nil {
		return err
	}
	defer clear(plain)
	return core.AtomicWrite(filepath.Clean(destination), plain, 0600)
}
