package core

import (
	"akasha/internal/resources"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type ImportReport struct {
	Files      int      `json:"files"`
	Changed    int      `json:"changed"`
	Unchanged  int      `json:"unchanged"`
	Deleted    int      `json:"deleted"`
	Skipped    []string `json:"skipped"`
	Conflicts  []string `json:"conflicts"`
	Checkpoint string   `json:"checkpoint"`
}

var ignoredDirs = map[string]bool{".git": true, ".akasha": true, ".akasha-conflicts": true, "node_modules": true, "vendor": true, ".venv": true, "venv": true, "__pycache__": true, "dist": true, "build": true, "bin": true, ".next": true, ".idea": true, ".vscode": true}

func SensitivePath(rel string) bool {
	lower := strings.ToLower(rel)
	base := path.Base(lower)
	if base == ".env" || strings.HasPrefix(base, ".env.") || strings.Contains(base, "credentials") || strings.Contains(base, "secret") || base == "id_rsa" || base == "id_ed25519" {
		return true
	}
	for _, ext := range []string{".key", ".pem", ".p12", ".pfx", ".kdbx", ".token", ".akasha-key", ".db", ".sqlite", ".sqlite3"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}
func excluded(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if ignoredDirs[strings.ToLower(part)] || strings.EqualFold(part, ".codex") {
			return true
		}
	}
	return SensitivePath(rel)
}
func SourceFiles(ctx context.Context, root string) ([]string, error) {
	if root == "" {
		return nil, errors.New("project has no local source root")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, errors.New("source root is unavailable")
	}
	files := []string{}
	// Ask Git only for its inventory; never run hooks, builds, or project programs.
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	output, e := cmd.Output()
	if e == nil {
		for _, name := range strings.Split(string(output), "\x00") {
			if name != "" && !excluded(name) {
				files = append(files, name)
			}
		}
	} else {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if e = ctx.Err(); e != nil {
				return e
			}
			rel, e := filepath.Rel(root, p)
			if e != nil {
				return e
			}
			rel = filepath.ToSlash(rel)
			if rel == "." {
				return nil
			}
			if excluded(rel) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.IsDir() {
				files = append(files, rel)
			}
			if len(files) > 20000 {
				return errors.New("source exceeds 20000 files; narrow the project root")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(files) > 20000 {
		return nil, errors.New("source exceeds 20000 files; narrow the project root")
	}
	sort.Strings(files)
	return files, nil
}
func textSource(rel string) bool {
	switch strings.ToLower(path.Ext(rel)) {
	case ".go", ".rs", ".c", ".h", ".cpp", ".hpp", ".cc", ".cs", ".java", ".kt", ".py", ".js", ".ts", ".jsx", ".tsx", ".vue", ".svelte", ".md", ".mdx", ".txt", ".json", ".yaml", ".yml", ".toml", ".sql", ".html", ".css", ".sh", ".ps1", ".xml", ".proto":
		return true
	}
	base := path.Base(rel)
	return base == "Dockerfile" || base == "Makefile" || base == "go.mod" || base == "AGENTS.md"
}
func ReadSource(root, rel string, maxBytes int64) ([]byte, error) {
	p, err := SafePath(root, rel)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, errors.New("file is not regular or exceeds size limit")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxBytes {
		return nil, errors.New("file grew beyond size limit")
	}
	return b, nil
}
func (s *Store) Import(ctx context.Context, author string) (ImportReport, error) {
	report := ImportReport{Skipped: []string{}, Conflicts: []string{}}
	files, err := SourceFiles(ctx, s.Project.Root)
	if err != nil {
		return report, err
	}
	seen := map[string]bool{}
	save := func(in WriteInput) error {
		seen[in.Entity] = true
		heads, e := s.Heads(ctx, in.Entity)
		if e != nil {
			return e
		}
		if len(heads) > 1 {
			report.Conflicts = append(report.Conflicts, in.Entity)
			return nil
		}
		in.Expected = []string{}
		for _, h := range heads {
			in.Expected = append(in.Expected, h.ID)
		}
		_, changed, e := s.Put(ctx, author, in)
		if e != nil {
			return e
		}
		if changed {
			report.Changed++
		} else {
			report.Unchanged++
		}
		return nil
	}
	folders := map[string]bool{}
	profile := resources.Default()
	for start := 0; start < len(files); start += profile.ImportWindow {
		batch := files[start:min(start+profile.ImportWindow, len(files))]
		prepared := make([]preparedSource, len(batch))
		resources.Parallel(ctx, len(batch), profile.Workers, func(i int) { prepared[i] = prepareSource(s.Project.Root, batch[i]) })
		for i, rel := range batch {
			if err = ctx.Err(); err != nil {
				return report, err
			}
			if !textSource(rel) {
				continue
			}
			p := prepared[i]
			if p.err != nil {
				report.Skipped = append(report.Skipped, rel+": "+p.err.Error())
				seen["source/"+rel] = true
				continue
			}
			report.Files++
			dir := path.Dir(rel)
			folders[dir] = true
			for _, in := range p.inputs {
				if err = save(in); err != nil {
					return report, err
				}
			}
		}
	}
	folderList := []string{}
	for dir := range folders {
		for {
			folders[dir] = true
			parent := path.Dir(dir)
			if parent == dir || dir == "." {
				break
			}
			dir = parent
		}
	}
	for dir := range folders {
		folderList = append(folderList, dir)
	}
	sort.Strings(folderList)
	for _, dir := range folderList {
		links := []Link{}
		if dir != "." {
			links = append(links, Link{Target: "folder/" + path.Dir(dir), Relation: "inside"})
		}
		if err = save(WriteInput{Entity: "folder/" + dir, Kind: "note", Title: dir, Text: "Directory in imported project: " + dir, Links: links, Meta: map[string]string{"importer": "code-v1"}}); err != nil {
			return report, err
		}
	}
	// A partial scan must not infer deletions of skipped or unreadable sources.
	if len(report.Skipped) == 0 {
		records, e := s.List(ctx, "", 10000)
		if e != nil {
			return report, e
		}
		if len(records) == 10000 {
			return report, errors.New("deletion scan limit reached; no deletion inferred")
		}
		for _, r := range records {
			if r.Meta["importer"] != "code-v1" || seen[r.Entity] {
				continue
			}
			heads, e := s.Heads(ctx, r.Entity)
			if e != nil {
				return report, e
			}
			if len(heads) != 1 {
				report.Conflicts = append(report.Conflicts, r.Entity)
				continue
			}
			_, changed, e := s.Put(ctx, author, WriteInput{Entity: r.Entity, Kind: r.Kind, Title: r.Title, Source: r.Source, Expected: []string{r.ID}, Deleted: true, Meta: r.Meta})
			if e != nil {
				return report, e
			}
			if changed {
				report.Deleted++
			}
		}
	}
	report.Checkpoint, err = s.Checkpoint(ctx, "import")
	return report, err
}

type preparedSource struct {
	inputs []WriteInput
	err    error
}

func prepareSource(root, rel string) preparedSource {
	if !textSource(rel) {
		return preparedSource{}
	}
	data, e := ReadSource(root, rel, 2<<20)
	if e != nil {
		return preparedSource{err: e}
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), '\x00') {
		return preparedSource{err: errors.New("binary content")}
	}
	kind := "code"
	if strings.HasSuffix(strings.ToLower(rel), ".md") || strings.HasSuffix(rel, ".txt") {
		kind = "note"
	}
	links := []Link{{Target: "folder/" + path.Dir(rel), Relation: "inside"}}
	var symbols []WriteInput
	if strings.HasSuffix(rel, ".go") {
		symbols, links = goSymbols(rel, data, links)
	} else {
		symbols = lexicalSymbols(rel, string(data))
	}
	in := WriteInput{Entity: "source/" + rel, Kind: kind, Title: rel, Text: Redact(string(data)), Source: rel, Links: links, Meta: map[string]string{"importer": "code-v1", "sha256": Hash(string(data)), "language": path.Ext(rel)}}
	return preparedSource{inputs: append([]WriteInput{in}, symbols...)}
}
func goSymbols(rel string, data []byte, links []Link) ([]WriteInput, []Link) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, data, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return lexicalSymbols(rel, string(data)), links
	}
	out := []WriteInput{}
	for _, im := range file.Imports {
		v, e := strconv.Unquote(im.Path.Value)
		if e == nil {
			links = append(links, Link{Target: v, Relation: "imports"})
		}
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				start, end := fset.Position(d.Recv.List[0].Type.Pos()).Offset, fset.Position(d.Recv.List[0].Type.End()).Offset
				name = string(data[start:end]) + "." + name
			}
			start, end := fset.Position(d.Pos()).Offset, fset.Position(d.End()).Offset
			if d.Body != nil {
				end = fset.Position(d.Body.Pos()).Offset
			}
			signature := strings.TrimSpace(string(data[start:end]))
			if d.Doc != nil {
				signature = d.Doc.Text() + signature
			}
			row := fset.Position(d.Pos()).Line
			out = append(out, WriteInput{Entity: "symbol/" + rel + "#" + name, Kind: "symbol", Title: name, Text: signature, Source: fmt.Sprintf("%s:%d", rel, row), Links: []Link{{Target: "source/" + rel, Relation: "defined_in"}}, Meta: map[string]string{"importer": "code-v1", "parser": "go/ast"}})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				start, end := fset.Position(ts.Pos()).Offset, fset.Position(ts.End()).Offset
				out = append(out, WriteInput{Entity: "symbol/" + rel + "#" + ts.Name.Name, Kind: "symbol", Title: ts.Name.Name, Text: string(data[start:end]), Source: fmt.Sprintf("%s:%d", rel, fset.Position(ts.Pos()).Line), Links: []Link{{Target: "source/" + rel, Relation: "defined_in"}}, Meta: map[string]string{"importer": "code-v1", "parser": "go/ast"}})
			}
		}
	}
	return out, links
}

var lexicalDefinition = regexp.MustCompile(`(?m)^\s*(?:(?:export|public|private|static|async|pub)\s+)*(?:function|class|interface|def|fn|struct|enum|type)\s+([A-Za-z_][A-Za-z0-9_]*)[^\n]{0,220}`)

func lexicalSymbols(rel, text string) []WriteInput {
	out := []WriteInput{}
	seen := map[string]bool{}
	for _, m := range lexicalDefinition.FindAllStringSubmatchIndex(text, 500) {
		name := text[m[2]:m[3]]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, WriteInput{Entity: "symbol/" + rel + "#" + name, Kind: "symbol", Title: name, Text: strings.TrimSpace(text[m[0]:m[1]]), Source: fmt.Sprintf("%s:%d", rel, strings.Count(text[:m[0]], "\n")+1), Links: []Link{{Target: "source/" + rel, Relation: "defined_in"}}, Meta: map[string]string{"importer": "code-v1", "parser": "lexical-not-semantic"}})
	}
	return out
}
