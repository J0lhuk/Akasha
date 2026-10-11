package core

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".akasha-write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type checkpointHeader struct {
	Schema    int    `json:"schema"`
	ProjectID string `json:"project_id"`
	SpaceID   string `json:"space_id"`
	Created   string `json:"created"`
	Reason    string `json:"reason"`
	Revision  int64  `json:"revision"`
}

func (s *Store) Checkpoint(ctx context.Context, reason string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	revision, err := s.Revision(ctx)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(s.Dir(), "checkpoints")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, ".checkpoint-*")
	if err != nil {
		return "", err
	}
	temp := file.Name()
	defer os.Remove(temp)
	gz := gzip.NewWriter(file)
	encoder := json.NewEncoder(gz)
	fail := func(e error) (string, error) { gz.Close(); file.Close(); return "", e }
	if err = encoder.Encode(checkpointHeader{1, s.Project.ID, s.Project.SpaceID, Now(), Redact(reason), revision}); err != nil {
		return fail(err)
	}
	after := int64(0)
	for {
		events, e := s.Events(ctx, after, 200)
		if e != nil {
			return fail(e)
		}
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			if err = encoder.Encode(event.Record); err != nil {
				return fail(err)
			}
			after = event.Sequence
		}
	}
	if err = gz.Close(); err != nil {
		file.Close()
		return "", err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	name := time.Now().UTC().Format("20060102T150405.000000000") + fmt.Sprintf("-%d.jsonl.gz", revision)
	dst := filepath.Join(dir, name)
	if err = os.Rename(temp, dst); err != nil {
		return "", err
	}
	digest, err := FileHash(dst)
	if err != nil {
		return dst, err
	}
	if err = AtomicWrite(dst+".sha256", []byte(digest+"  "+name+"\n"), 0600); err != nil {
		return dst, err
	}
	// File checkpoints contain immutable event snapshots, not an unsafe copy of an open WAL DB.
	return dst, nil
}
func FileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (s *Store) RestoreCheckpoint(ctx context.Context, path string) (int, error) {
	sum, err := os.ReadFile(path + ".sha256")
	if err != nil {
		return 0, err
	}
	parts := strings.Fields(string(sum))
	actual, err := FileHash(path)
	if err != nil {
		return 0, err
	}
	if len(parts) < 1 || parts[0] != actual {
		return 0, errors.New("checkpoint checksum mismatch")
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	defer gz.Close()
	limited := &io.LimitedReader{R: gz, N: (256 << 20) + 1}
	dec := json.NewDecoder(limited)
	var header checkpointHeader
	if err = dec.Decode(&header); err != nil {
		return 0, err
	}
	if header.Schema != 1 || header.ProjectID != s.Project.ID || header.SpaceID != s.Project.SpaceID {
		return 0, errors.New("checkpoint belongs to another project or schema")
	}
	records := []Record{}
	for {
		var r Record
		if err = dec.Decode(&r); err == io.EOF {
			break
		} else if err != nil {
			return 0, err
		}
		if limited.N <= 0 {
			return 0, errors.New("checkpoint exceeds 256 MiB decoded size")
		}
		records = append(records, r)
		if len(records) > 10000 {
			return 0, errors.New("checkpoint import exceeds 10000 revisions")
		}
	}
	if limited.N <= 0 {
		return 0, errors.New("checkpoint exceeds 256 MiB decoded size")
	}
	return s.Merge(ctx, records)
}

type WikiResult struct {
	Written   int      `json:"written"`
	Imported  int      `json:"imported"`
	Conflicts []string `json:"conflicts"`
}
type wikiEntry struct {
	Entity   string `json:"entity"`
	Revision string `json:"revision"`
	SHA256   string `json:"sha256"`
	Kind     string `json:"kind"`
}

func (s *Store) wikiManifest() (map[string]wikiEntry, error) {
	var manifest map[string]wikiEntry
	data, err := os.ReadFile(filepath.Join(s.Dir(), "wiki", "manifest.json"))
	if os.IsNotExist(err) {
		return map[string]wikiEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(data, &manifest)
	return manifest, err
}
func wikiName(entity string) string {
	parts := strings.Split(entity, "/")
	for i, v := range parts {
		v = strings.Map(func(r rune) rune {
			if r < ' ' || strings.ContainsRune(`\:*?"<>|`, r) {
				return '_'
			}
			return r
		}, v)
		v = strings.Trim(v, " .")
		if v == "" || v == ".." {
			v = "note"
		}
		if len([]rune(v)) > 50 {
			v = string([]rune(v)[:50])
		}
		parts[i] = v
	}
	return strings.Join(parts, "/") + "-" + Hash(entity)[:8] + ".md"
}
func (s *Store) ExportWiki(ctx context.Context) (WikiResult, error) {
	result := WikiResult{Conflicts: []string{}}
	manifest, err := s.wikiManifest()
	if err != nil {
		return result, err
	}
	records, err := s.List(ctx, "", 10000)
	if err != nil {
		return result, err
	}
	dir := filepath.Join(s.Dir(), "wiki")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return result, err
	}
	for _, r := range records {
		if r.Kind != "note" && r.Kind != "decision" && r.Kind != "task" {
			continue
		}
		heads, e := s.Heads(ctx, r.Entity)
		if e != nil {
			return result, e
		}
		if len(heads) != 1 {
			result.Conflicts = append(result.Conflicts, r.Entity)
			continue
		}
		name := wikiName(r.Entity)
		dst, e := SafePath(dir, name)
		if e != nil {
			return result, e
		}
		prior := manifest[name]
		existing, e := os.ReadFile(dst)
		if e == nil && (prior.SHA256 == "" || Hash(string(existing)) != prior.SHA256) {
			result.Conflicts = append(result.Conflicts, name)
			continue
		}
		if e != nil && !os.IsNotExist(e) {
			return result, e
		}
		body := "# " + strings.ReplaceAll(r.Title, "\n", " ") + "\n\n" + r.Text + "\n"
		if err = AtomicWrite(dst, []byte(body), 0600); err != nil {
			return result, err
		}
		manifest[name] = wikiEntry{r.Entity, r.ID, Hash(body), r.Kind}
		result.Written++
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return result, err
	}
	err = AtomicWrite(filepath.Join(dir, "manifest.json"), data, 0600)
	return result, err
}
func (s *Store) ImportWiki(ctx context.Context, author string) (WikiResult, error) {
	result := WikiResult{Conflicts: []string{}}
	manifest, err := s.wikiManifest()
	if err != nil {
		return result, err
	}
	dir := filepath.Join(s.Dir(), "wiki")
	for name, entry := range manifest {
		p, e := SafePath(dir, name)
		if e != nil {
			return result, e
		}
		data, e := os.ReadFile(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return result, e
		}
		if len(data) > 2<<20 {
			return result, errors.New("wiki page exceeds size limit")
		}
		if Hash(string(data)) == entry.SHA256 {
			continue
		}
		original, e := s.Get(ctx, entry.Revision)
		if e != nil {
			return result, e
		}
		if original.Entity != entry.Entity {
			return result, errors.New("wiki manifest entity mismatch")
		}
		parts := strings.SplitN(string(data), "\n", 3)
		if len(parts) < 3 || !strings.HasPrefix(parts[0], "# ") {
			return result, errors.New("wiki page must start with a title and blank line")
		}
		r, _, e := s.Put(ctx, author, WriteInput{Entity: original.Entity, Kind: original.Kind, Title: strings.TrimPrefix(parts[0], "# "), Text: strings.TrimSuffix(parts[2], "\n"), Expected: []string{entry.Revision}, Source: original.Source, Tags: original.Tags, Links: original.Links, Meta: original.Meta})
		if errors.Is(e, ErrConflict) {
			result.Conflicts = append(result.Conflicts, name)
			continue
		}
		if e != nil {
			return result, e
		}
		manifest[name] = wikiEntry{r.Entity, r.ID, Hash(string(data)), r.Kind}
		result.Imported++
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return result, err
	}
	err = AtomicWrite(filepath.Join(dir, "manifest.json"), data, 0600)
	return result, err
}
