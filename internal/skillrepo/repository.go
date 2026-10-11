// Package skillrepo stores explicitly published skill packages as immutable,
// content-addressed versions. It never executes their instructions or scripts.
package skillrepo

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"akasha/internal/core"
	"go.yaml.in/yaml/v3"
)

const maxArchive = 8 << 20
const maxExpanded = 16 << 20
const manifestName = ".akasha-skill-manifest.json"

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type Metadata struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
}
type File struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Size       int    `json:"size"`
	Executable bool   `json:"executable"`
}
type Manifest struct {
	Format   string   `json:"format"`
	Metadata Metadata `json:"metadata"`
	Files    []File   `json:"files"`
	SHA256   string   `json:"sha256"`
}
type Version struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Label       string `json:"label,omitempty"`
	SHA256      string `json:"sha256"`
	Created     string `json:"created"`
	Author      string `json:"author"`
	Files       int    `json:"files"`
	Bytes       int    `json:"archive_bytes"`
}
type Installed struct {
	Metadata
	Path  string `json:"path"`
	Error string `json:"error,omitempty"`
}

func ParseMetadata(b []byte) (Metadata, error) {
	text := strings.TrimPrefix(strings.ReplaceAll(string(b), "\r\n", "\n"), "\ufeff")
	if !strings.HasPrefix(text, "---\n") {
		return Metadata{}, errors.New("SKILL.md requires YAML frontmatter")
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 || end > 64<<10 {
		return Metadata{}, errors.New("invalid skill frontmatter")
	}
	var m Metadata
	if err := yaml.Unmarshal([]byte(text[4:4+end]), &m); err != nil {
		return m, err
	}
	if !validName.MatchString(m.Name) || strings.TrimSpace(m.Description) == "" || len(m.Description) > 8000 {
		return m, errors.New("skill requires standard name and description up to 8000 bytes")
	}
	return m, nil
}

func readBounded(name string, limit int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("skill file exceeds size limit")
	}
	return b, nil
}

func Inspect(directory string) (Metadata, error) {
	b, err := readBounded(filepath.Join(directory, "SKILL.md"), 1<<20)
	if err != nil {
		return Metadata{}, err
	}
	return ParseMetadata(b)
}

func InstalledSkills(roots []string) ([]Installed, error) {
	out := []Installed{}
	for _, root := range roots {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		visited := 0
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			visited++
			if visited > 50000 {
				return errors.New("skill inventory exceeds 50000 entries; select a narrower root")
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() && p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "__pycache__") {
				return filepath.SkipDir
			}
			if !d.IsDir() && d.Name() == "SKILL.md" {
				m, e := Inspect(filepath.Dir(p))
				item := Installed{Metadata: m, Path: filepath.Dir(p)}
				if e != nil {
					item.Error = e.Error()
				}
				out = append(out, item)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Path < out[j].Path
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func Pack(directory string) (Manifest, []byte, error) {
	var m Manifest
	abs, err := filepath.Abs(directory)
	if err != nil {
		return m, nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return m, nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return m, nil, errors.New("skill root must be a regular directory")
	}
	abs, err = core.CanonicalDirectory(abs)
	if err != nil {
		return m, nil, err
	}
	metadata, err := Inspect(abs)
	if err != nil {
		return m, nil, err
	}
	m = Manifest{Format: "akasha-skill-v1", Metadata: metadata, Files: []File{}}
	contents := map[string][]byte{}
	total := 0
	seen := map[string]bool{}
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if p == abs {
			return nil
		}
		rel, e := filepath.Rel(abs, p)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill symlink rejected: %s", rel)
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "__pycache__" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == manifestName {
			return nil
		}
		if _, e = core.SafePath(abs, rel); e != nil {
			return e
		}
		if core.SensitivePath(rel) {
			return fmt.Errorf("sensitive file excluded from skill publication: %s", rel)
		}
		if seen[strings.ToLower(rel)] {
			return errors.New("case-colliding file paths")
		}
		seen[strings.ToLower(rel)] = true
		info, e := d.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return errors.New("special skill file rejected")
		}
		b, e := readBounded(p, 4<<20)
		if e != nil {
			return e
		}
		if utf8.Valid(b) && core.Redact(string(b)) != string(b) {
			return fmt.Errorf("possible credential in skill file; inspect before publication: %s", rel)
		}
		total += len(b)
		if total > maxExpanded || len(m.Files) >= 512 {
			return errors.New("skill exceeds 512 files or 16 MiB expanded")
		}
		m.Files = append(m.Files, File{Path: rel, SHA256: core.Hash(string(b)), Size: len(b), Executable: info.Mode()&0111 != 0})
		contents[rel] = b
		return nil
	})
	if err != nil {
		return m, nil, err
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	canonical, err := json.Marshal(m)
	if err != nil {
		return m, nil, err
	}
	m.SHA256 = core.Hash(string(canonical))
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, file := range m.Files {
		header := &zip.FileHeader{Name: file.Path, Method: zip.Deflate}
		mode := os.FileMode(0600)
		if file.Executable {
			mode = 0700
		}
		header.SetMode(mode)
		w, e := zw.CreateHeader(header)
		if e != nil {
			return m, nil, e
		}
		if _, e = w.Write(contents[file.Path]); e != nil {
			return m, nil, e
		}
	}
	manifest, err := json.Marshal(m)
	if err != nil {
		return m, nil, err
	}
	w, err := zw.Create(manifestName)
	if err != nil {
		return m, nil, err
	}
	if _, err = w.Write(manifest); err != nil {
		return m, nil, err
	}
	if err = zw.Close(); err != nil {
		return m, nil, err
	}
	if buf.Len() > maxArchive {
		return m, nil, errors.New("compressed skill exceeds 8 MiB")
	}
	return m, buf.Bytes(), nil
}

func unpack(archive []byte, expectedHash string) (Manifest, map[string][]byte, error) {
	var manifest Manifest
	if len(archive) > maxArchive {
		return manifest, nil, errors.New("skill archive exceeds 8 MiB")
	}
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return manifest, nil, err
	}
	if len(zr.File) > 513 {
		return manifest, nil, errors.New("too many skill files")
	}
	files := map[string][]byte{}
	seen := map[string]bool{}
	total := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !f.Mode().IsRegular() || f.UncompressedSize64 > 4<<20 || f.Name != path.Clean(f.Name) {
			return manifest, nil, errors.New("invalid skill archive entry")
		}
		if _, err = core.SafePath(os.TempDir(), f.Name); err != nil {
			return manifest, nil, err
		}
		if seen[strings.ToLower(f.Name)] {
			return manifest, nil, errors.New("duplicate skill archive entry")
		}
		seen[strings.ToLower(f.Name)] = true
		r, e := f.Open()
		if e != nil {
			return manifest, nil, e
		}
		b, e := io.ReadAll(io.LimitReader(r, 4<<20+1))
		r.Close()
		if e != nil {
			return manifest, nil, e
		}
		total += len(b)
		if len(b) > 4<<20 || total > maxExpanded+256<<10 {
			return manifest, nil, errors.New("expanded skill exceeds limit")
		}
		files[f.Name] = b
	}
	if err = json.Unmarshal(files[manifestName], &manifest); err != nil {
		return manifest, nil, err
	}
	delete(files, manifestName)
	if manifest.Format != "akasha-skill-v1" || len(manifest.Files) != len(files) {
		return manifest, nil, errors.New("skill manifest mismatch")
	}
	claimed := manifest.SHA256
	manifest.SHA256 = ""
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return manifest, nil, err
	}
	manifest.SHA256 = claimed
	if claimed != core.Hash(string(canonical)) || claimed != expectedHash {
		return manifest, nil, errors.New("skill content hash mismatch")
	}
	seen = map[string]bool{}
	for _, f := range manifest.Files {
		b, ok := files[f.Path]
		if !ok || seen[f.Path] || len(b) != f.Size || core.Hash(string(b)) != f.SHA256 {
			return manifest, nil, errors.New("skill file checksum mismatch")
		}
		seen[f.Path] = true
		if core.SensitivePath(f.Path) {
			return manifest, nil, errors.New("sensitive file in skill package")
		}
	}
	m, err := ParseMetadata(files["SKILL.md"])
	if err != nil {
		return manifest, nil, err
	}
	if m != manifest.Metadata {
		return manifest, nil, errors.New("skill metadata mismatch")
	}
	return manifest, files, nil
}

func Publish(ctx context.Context, s *core.Store, author, directory, label string) (Version, bool, error) {
	if len(label) > 200 {
		return Version{}, false, errors.New("version label exceeds 200 bytes")
	}
	m, blob, err := Pack(directory)
	if err != nil {
		return Version{}, false, err
	}
	entity := "skills/" + m.Metadata.Name + "/" + m.SHA256
	heads, err := s.Heads(ctx, entity)
	if err != nil {
		return Version{}, false, err
	}
	if len(heads) > 0 {
		for _, r := range heads {
			if r.Kind == "skill_bundle" && !r.Deleted && r.Meta["sha256"] == m.SHA256 {
				return version(r), false, nil
			}
		}
		return Version{}, false, core.ErrConflict
	}
	r, changed, err := s.Put(ctx, author, core.WriteInput{Entity: entity, Kind: "skill_bundle", Title: m.Metadata.Name, Text: core.Redact(m.Metadata.Description), Blob: blob, Meta: map[string]string{"sha256": m.SHA256, "label": core.Redact(label), "files": fmt.Sprint(len(m.Files))}})
	if err != nil {
		return Version{}, false, err
	}
	return version(r), changed, nil
}

func version(r core.Record) Version {
	n := 0
	_, _ = fmt.Sscan(r.Meta["files"], &n)
	return Version{ID: r.ID, Name: r.Title, Description: r.Text, Label: r.Meta["label"], SHA256: r.Meta["sha256"], Created: r.Created, Author: r.Author, Files: n, Bytes: len(r.Blob)}
}
func Catalog(ctx context.Context, s *core.Store, name string) ([]Version, error) {
	rows, err := s.List(ctx, "skill_bundle", 10000)
	if err != nil {
		return nil, err
	}
	versions := []Version{}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Deleted || (name != "" && r.Title != name) {
			continue
		}
		key := r.Title + "/" + r.Meta["sha256"]
		if seen[key] {
			continue
		}
		seen[key] = true
		versions = append(versions, version(r))
	}
	return versions, nil
}

type InstallResult struct {
	Version Version `json:"version"`
	Path    string  `json:"path"`
	Backup  string  `json:"backup,omitempty"`
	Changed bool    `json:"changed"`
}

func Install(ctx context.Context, s *core.Store, id, destination string, replace bool) (InstallResult, error) {
	result := InstallResult{}
	r, err := s.Get(ctx, id)
	if err != nil {
		return result, err
	}
	if r.Kind != "skill_bundle" || r.Deleted {
		return result, core.ErrNotFound
	}
	m, files, err := unpack(r.Blob, r.Meta["sha256"])
	if err != nil {
		return result, err
	}
	if destination == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return result, e
		}
		base := os.Getenv("CODEX_HOME")
		if base == "" {
			base = filepath.Join(home, ".codex")
		}
		destination = filepath.Join(base, "skills", m.Metadata.Name)
	}
	abs, err := filepath.Abs(destination)
	if err != nil {
		return result, err
	}
	parent := filepath.Dir(abs)
	if _, err = core.SafePath(filepath.VolumeName(abs)+string(filepath.Separator), filepath.ToSlash(strings.TrimPrefix(strings.TrimPrefix(abs, filepath.VolumeName(abs)), string(filepath.Separator)))); err != nil {
		return result, err
	}
	result.Version = version(r)
	result.Path = abs
	exists := false
	if info, e := os.Lstat(abs); e == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return result, errors.New("install destination is not a regular directory")
		}
		exists = true
		current, _, e := Pack(abs)
		if e == nil && current.SHA256 == m.SHA256 {
			return result, nil
		}
		if !replace {
			return result, errors.New("installed skill differs; choose another directory or explicitly use --replace")
		}
		if _, e = Inspect(abs); e != nil {
			return result, errors.New("refusing to replace a directory without a valid SKILL.md")
		}
	} else if !os.IsNotExist(e) {
		return result, e
	}
	if err = os.MkdirAll(parent, 0700); err != nil {
		return result, err
	}
	stage, err := os.MkdirTemp(parent, ".akasha-install-")
	if err != nil {
		return result, err
	}
	defer func() {
		if core.Within(parent, stage) && filepath.Dir(stage) == parent {
			_ = os.RemoveAll(stage)
		}
	}()
	for _, f := range m.Files {
		dst, e := core.SafePath(stage, f.Path)
		if e != nil {
			return result, e
		}
		mode := os.FileMode(0600)
		if f.Executable {
			mode = 0700
		}
		if e = core.AtomicWrite(dst, files[f.Path], mode); e != nil {
			return result, e
		}
	}
	if exists {
		backup := filepath.Join(parent, ".akasha-backup-"+filepath.Base(abs)+"-"+core.NewID())
		if !core.Within(parent, backup) || filepath.Dir(abs) != parent {
			return result, errors.New("unsafe backup path")
		}
		if err = renameDirectory(ctx, abs, backup); err != nil {
			return result, err
		}
		result.Backup = backup
	}
	if err = renameDirectory(ctx, stage, abs); err != nil {
		if result.Backup != "" {
			recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			restoreErr := renameDirectory(recovery, result.Backup, abs)
			cancel()
			if restoreErr != nil {
				return result, errors.Join(err, fmt.Errorf("previous skill remains in backup %s: %w", result.Backup, restoreErr))
			}
		}
		return result, err
	}
	result.Changed = true
	return result, nil
}
