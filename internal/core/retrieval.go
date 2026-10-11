package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Hit struct {
	ProjectID string  `json:"project_id"`
	ID        string  `json:"id"`
	Entity    string  `json:"entity"`
	Kind      string  `json:"kind"`
	Title     string  `json:"title"`
	Snippet   string  `json:"snippet"`
	Score     float64 `json:"score"`
	Created   string  `json:"created,omitempty"`
	Dense     bool    `json:"dense,omitempty"`
}

var words = regexp.MustCompile(`[\p{L}\p{N}_]+`)
var wikiLink = regexp.MustCompile(`\[\[([^\]\n]+)\]\]`)
var asciiIdentifier = regexp.MustCompile(`^[A-Za-z0-9_./\\:$-]+$`)
var camelIdentifier = regexp.MustCompile(`[a-z][A-Z]`)

// A lone code symbol/path or opaque identifier asks for lexical evidence.
// Dense neighbours cannot establish that an unknown identifier exists.
// Natural-language questions still use hybrid retrieval; VectorSearch is an
// explicit dense-only operation and intentionally does not apply this policy.
func exactIdentifierQuery(q string) bool {
	q = strings.TrimSpace(q)
	return asciiIdentifier.MatchString(q) && (len(q) >= 16 ||
		strings.ContainsAny(q, "_./\\:$-0123456789") || camelIdentifier.MatchString(q))
}

func queryTerms(q string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, w := range words.FindAllString(strings.ToLower(q), -1) {
		if len(w) > 1 && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
		if len(out) == 16 {
			break
		}
	}
	return out
}
func ftsQuery(q string) string {
	terms := queryTerms(q)
	for i := range terms {
		terms[i] = `"` + terms[i] + `"*`
	}
	return strings.Join(terms, " OR ")
}
func (s *Store) Search(ctx context.Context, q string, limit int) ([]Hit, error) {
	return s.search(ctx, q, limit, false)
}
func (s *Store) search(ctx context.Context, q string, limit int, evidenceOnly bool) ([]Hit, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	match := ftsQuery(q)
	if match == "" {
		return []Hit{}, nil
	}
	// Code identifiers must survive BM25 length normalization of long source files.
	// This is a preference within authorized FTS matches, not an additional scope.
	identifiers := []string{}
	for _, term := range queryTerms(q) {
		if strings.Contains(term, "_") {
			identifiers = append(identifiers, `"`+term+`"*`)
		}
	}
	order := `bm25(search,0,5,1,2),r.id`
	args := []any{match}
	if len(identifiers) > 0 {
		order = `CASE WHEN r.id IN (SELECT id FROM search WHERE search MATCH ?) THEN 0 ELSE 1 END,` + order
		args = append(args, strings.Join(identifiers, " OR "))
	}
	args = append(args, limit)
	filter := ""
	if evidenceOnly {
		filter = " AND r.kind NOT IN ('prompt','tool') "
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.entity,r.kind,r.title,snippet(search,2,'','', ' … ',28),bm25(search,0,5,1,2) FROM search JOIN records r ON r.id=search.id WHERE search MATCH ? AND r.deleted=0 AND `+headPredicate+filter+` ORDER BY `+order+` LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	rank := 0
	for rows.Next() {
		var h Hit
		var bm float64
		if err = rows.Scan(&h.ID, &h.Entity, &h.Kind, &h.Title, &h.Snippet, &bm); err != nil {
			return nil, err
		}
		rank++
		h.ProjectID = s.Project.ID
		h.Score = 1/float64(rank) + math.Min(-bm, 20)*0.01
		out = append(out, h)
	}
	return out, rows.Err()
}

type GraphNode struct {
	ID       string  `json:"id"`
	Entity   string  `json:"entity"`
	Title    string  `json:"title"`
	Kind     string  `json:"kind"`
	Rank     float64 `json:"rank"`
	Conflict bool    `json:"conflict"`
}
type GraphEdge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
}
type Graph struct {
	Revision  int64       `json:"revision"`
	Nodes     []GraphNode `json:"nodes"`
	Edges     []GraphEdge `json:"edges"`
	Truncated bool        `json:"truncated"`
}

func (s *Store) Graph(ctx context.Context) (Graph, error) {
	revision, err := s.KnowledgeRevision(ctx)
	if err != nil {
		return Graph{}, err
	}
	if raw, ok, e := s.cacheGet(ctx, "graph-v1", revision); e != nil {
		return Graph{}, e
	} else if ok {
		var g Graph
		e = json.Unmarshal([]byte(raw), &g)
		return g, e
	}
	records, err := s.Knowledge(ctx, 10000)
	if err != nil {
		return Graph{}, err
	}
	g := Graph{Revision: revision, Nodes: []GraphNode{}, Edges: []GraphEdge{}, Truncated: len(records) == 10000}
	lookup := map[string][]string{}
	counts := map[string]int{}
	for _, r := range records {
		if !graphKind(r.Kind) {
			continue
		}
		counts[r.Entity]++
		lookup[normalize(r.Entity)] = append(lookup[normalize(r.Entity)], r.ID)
		if normalize(r.Title) != normalize(r.Entity) {
			lookup[normalize(r.Title)] = append(lookup[normalize(r.Title)], r.ID)
		}
	}
	edges := map[string]bool{}
	for _, r := range records {
		if !graphKind(r.Kind) {
			continue
		}
		g.Nodes = append(g.Nodes, GraphNode{ID: r.ID, Entity: r.Entity, Title: r.Title, Kind: r.Kind, Conflict: counts[r.Entity] > 1})
		links := append([]Link{}, r.Links...)
		for _, v := range wikiLink.FindAllStringSubmatch(r.Text, -1) {
			target := strings.SplitN(strings.SplitN(v[1], "|", 2)[0], "#", 2)[0]
			links = append(links, Link{Target: target, Relation: "wiki"})
		}
		for _, l := range links {
			for _, id := range lookup[normalize(l.Target)] {
				if id == r.ID {
					continue
				}
				key := r.ID + ":" + id + ":" + l.Relation
				if !edges[key] {
					edges[key] = true
					g.Edges = append(g.Edges, GraphEdge{From: r.ID, To: id, Relation: l.Relation})
				}
			}
		}
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	sort.Slice(g.Edges, func(i, j int) bool {
		a, b := g.Edges[i], g.Edges[j]
		return a.From+a.To+a.Relation < b.From+b.To+b.Relation
	})
	ranks := pageRank(g, nil)
	for i := range g.Nodes {
		g.Nodes[i].Rank = ranks[g.Nodes[i].ID]
	}
	b, err := json.Marshal(g)
	if err != nil {
		return g, err
	}
	err = s.cachePut(ctx, "graph-v1", revision, string(b))
	return g, err
}
func graphKind(k string) bool {
	return searchable(k) && k != "prompt" && k != "tool" && k != "checkpoint"
}
func pageRank(g Graph, seeds map[string]float64) map[string]float64 {
	n := len(g.Nodes)
	rank := map[string]float64{}
	if n == 0 {
		return rank
	}
	base := map[string]float64{}
	total := 0.0
	for _, v := range seeds {
		total += v
	}
	for _, node := range g.Nodes {
		if total > 0 {
			base[node.ID] = seeds[node.ID] / total
		} else {
			base[node.ID] = 1 / float64(n)
		}
		rank[node.ID] = base[node.ID]
	}
	adj := map[string][]string{}
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e.To)
		adj[e.To] = append(adj[e.To], e.From)
	}
	for iteration := 0; iteration < 16; iteration++ {
		next := map[string]float64{}
		dangling := 0.0
		for id, v := range rank {
			if len(adj[id]) == 0 {
				dangling += v
			} else {
				for _, to := range adj[id] {
					next[to] += .75 * v / float64(len(adj[id]))
				}
			}
		}
		for id, b := range base {
			next[id] += (.25 + .75*dangling) * b
		}
		rank = next
	}
	return rank
}
func (s *Store) cacheGet(ctx context.Context, key string, revision int64) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM cache WHERE key=? AND revision=?", key, revision).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}
func (s *Store) cachePut(ctx context.Context, key string, revision int64, value string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO cache(key,revision,value,created) VALUES(?,?,?,?) ON CONFLICT(key) DO UPDATE SET revision=excluded.revision,value=excluded.value,created=excluded.created", key, revision, value, Now())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "DELETE FROM cache WHERE key IN (SELECT key FROM cache ORDER BY created DESC LIMIT -1 OFFSET 256)")
	return err
}
func (s *Store) Conflicts(ctx context.Context) ([]Conflict, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT r.entity FROM records r WHERE "+headPredicate+" GROUP BY r.entity HAVING count(*)>1 ORDER BY r.entity LIMIT 1000")
	if err != nil {
		return nil, err
	}
	entities := []string{}
	for rows.Next() {
		var entity string
		if err = rows.Scan(&entity); err != nil {
			rows.Close()
			return nil, err
		}
		entities = append(entities, entity)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Conflict{}
	for _, entity := range entities {
		heads, e := s.Heads(ctx, entity)
		if e != nil {
			return nil, e
		}
		if len(heads) < 2 {
			continue
		}
		conflict := heads[0].Kind != "fact"
		for i, a := range heads {
			for _, b := range heads[i+1:] {
				if a.Value != b.Value && intervalsOverlap(a, b) {
					conflict = true
				}
			}
		}
		if conflict {
			out = append(out, Conflict{Entity: entity, Kind: heads[0].Kind, Versions: heads})
		}
	}
	return out, nil
}
func intervalsOverlap(a, b Record) bool {
	parse := func(s string) time.Time { t, _ := time.Parse(time.RFC3339, s); return t }
	return (a.ValidTo == "" || b.ValidFrom == "" || parse(b.ValidFrom).Before(parse(a.ValidTo))) && (b.ValidTo == "" || a.ValidFrom == "" || parse(a.ValidFrom).Before(parse(b.ValidTo)))
}

// Scopes deliberately includes only this project and its readable shared project.
func (m *Manager) Scopes(p Principal, project string, shared bool) ([]*Store, error) {
	s, err := m.Store(p, project, "read")
	if err != nil {
		return nil, err
	}
	out := []*Store{s}
	if !shared || s.Project.Shared {
		return out, nil
	}
	js, err := m.Projects(p, s.Project.SpaceID)
	if err != nil {
		return nil, err
	}
	for _, j := range js {
		if j.Shared {
			ss, e := m.Store(p, j.ID, "read")
			if e != nil {
				return nil, e
			}
			out = append(out, ss)
		}
	}
	return out, nil
}
func (m *Manager) Search(ctx context.Context, p Principal, project, q string, shared bool, limit int) ([]Hit, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	stores, err := m.Scopes(p, project, shared)
	if err != nil {
		return nil, err
	}
	out := []Hit{}
	for _, s := range stores {
		hits, e := s.Search(ctx, q, limit)
		if e != nil {
			return nil, e
		}
		out = append(out, hits...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].ID < out[j].ID
		}
		return out[i].Score > out[j].Score
	})
	if !exactIdentifierQuery(q) && strings.TrimSpace(q) != "" && m.DenseAvailable() {
		dense, e := m.VectorSearch(ctx, p, project, q, shared, limit)
		if e != nil {
			return nil, e
		}
		return fuseHits(out, dense, limit), nil
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (m *Manager) PromptHistory(ctx context.Context, p Principal, space, project string, limit int) ([]Hit, error) {
	if project != "" {
		selected, err := m.Project(p, project, "read")
		if err != nil {
			return nil, err
		}
		if space != "" && selected.SpaceID != space {
			return nil, ErrDenied
		}
		space = selected.SpaceID
	}
	if space == "" {
		return nil, errors.New("prompt history requires a project or one group")
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	projects, err := m.Projects(p, space)
	if err != nil {
		return nil, err
	}
	out := []Hit{}
	for _, j := range projects {
		if project != "" && j.ID != project {
			continue
		}
		s, e := m.Store(p, j.ID, "read")
		if e != nil {
			return nil, e
		}
		records, e := s.List(ctx, "prompt", limit)
		if e != nil {
			return nil, e
		}
		for _, r := range records {
			out = append(out, Hit{ProjectID: j.ID, ID: r.ID, Entity: r.Entity, Kind: r.Kind, Title: r.Title, Snippet: preview(r.Text, 400), Created: r.Created})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created == out[j].Created {
			return out[i].ID < out[j].ID
		}
		return out[i].Created > out[j].Created
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (m *Manager) Transfer(ctx context.Context, p Principal, from, to, id, reason string) (Record, error) {
	if strings.TrimSpace(reason) == "" {
		return Record{}, errors.New("explicit transfer reason required")
	}
	source, err := m.Store(p, from, "admin")
	if err != nil {
		return Record{}, err
	}
	target, err := m.Store(p, to, "admin")
	if err != nil {
		return Record{}, err
	}
	r, err := source.Get(ctx, id)
	if err != nil {
		return r, err
	}
	if !graphKind(r.Kind) || r.Deleted {
		return r, errors.New("only selected public knowledge records can be transferred")
	}
	copied, _, err := target.Put(ctx, p.ID, WriteInput{Entity: "approved/" + NewID(), Kind: r.Kind, Title: r.Title, Text: r.Text, Subject: r.Subject, Predicate: r.Predicate, Value: r.Value, ValidFrom: r.ValidFrom, ValidTo: r.ValidTo, Source: "explicit transfer", Meta: map[string]string{"transfer_reason": Redact(reason)}})
	if err == nil {
		err = m.audit(p, "knowledge.transfer", from+":"+to+":"+id)
	}
	return copied, err
}
func preview(text string, n int) string {
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	return string(r[:n]) + "…"
}
