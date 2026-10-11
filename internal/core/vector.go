package core

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"akasha/internal/embedding"
	"akasha/internal/resources"
)

const vectorChunkVersion = "runes-480-overlap-80-single-v2"
const maxVectorChunks = 40000

type VectorStatus struct {
	Model           string `json:"model"`
	Dimensions      int    `json:"dimensions"`
	Chunks          int    `json:"chunks"`
	IndexedRevision int64  `json:"indexed_revision"`
	SourceRevision  int64  `json:"source_revision"`
	Stale           bool   `json:"stale"`
	VectorBytes     int64  `json:"vector_bytes"`
	CacheEntries    int    `json:"embedding_cache_entries"`
	CacheBytes      int64  `json:"embedding_cache_bytes"`
}
type VectorIndexReport struct {
	VectorStatus
	Embedded   int     `json:"embedded"`
	Reused     int     `json:"reused"`
	DurationMS float64 `json:"duration_ms"`
}

func (s *Store) ensureVectors() error {
	s.vectorOnce.Do(func() {
		_, s.vectorError = s.db.Exec(`CREATE TABLE IF NOT EXISTS vectors(model TEXT NOT NULL,record TEXT NOT NULL,chunk INTEGER NOT NULL,text TEXT NOT NULL,vector BLOB NOT NULL,PRIMARY KEY(model,record,chunk));
CREATE TABLE IF NOT EXISTS embedding_cache(model TEXT NOT NULL,hash TEXT NOT NULL,vector BLOB NOT NULL,created TEXT NOT NULL,PRIMARY KEY(model,hash));
CREATE TABLE IF NOT EXISTS vector_state(model TEXT PRIMARY KEY,revision INTEGER NOT NULL);`)
	})
	return s.vectorError
}
func vectorIdentity() string { return embedding.ModelID + "/" + vectorChunkVersion }
func (s *Store) VectorStatus(ctx context.Context) (VectorStatus, error) {
	v := VectorStatus{Model: vectorIdentity(), Dimensions: embedding.Dimension}
	if err := s.ensureVectors(); err != nil {
		return v, err
	}
	err := s.db.QueryRowContext(ctx, "SELECT coalesce(max(seq),0) FROM records WHERE kind IN ('note','code','symbol','fact','decision','task')").Scan(&v.SourceRevision)
	if err != nil {
		return v, err
	}
	err = s.db.QueryRowContext(ctx, "SELECT coalesce((SELECT revision FROM vector_state WHERE model=?),-1)", v.Model).Scan(&v.IndexedRevision)
	if err != nil {
		return v, err
	}
	err = s.db.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(length(vector)),0) FROM vectors WHERE model=?", v.Model).Scan(&v.Chunks, &v.VectorBytes)
	if err != nil {
		return v, err
	}
	err = s.db.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(length(vector)),0) FROM embedding_cache WHERE model=?", v.Model).Scan(&v.CacheEntries, &v.CacheBytes)
	v.Stale = v.SourceRevision != v.IndexedRevision
	return v, err
}
func (m *Manager) embedder() (*embedding.Engine, error) {
	m.embeddingMu.Lock()
	defer m.embeddingMu.Unlock()
	if m.embeddingEngine != nil {
		return m.embeddingEngine, nil
	}
	if _, err := embedding.Info(m.Root); err != nil {
		return nil, err
	}
	threads := resources.Default().InferenceThreads
	e, err := embedding.Open(m.Root, threads)
	if err != nil {
		return nil, err
	}
	m.embeddingEngine = e
	return e, nil
}
func (m *Manager) DenseAvailable() bool { _, err := embedding.Info(m.Root); return err == nil }

func encodeVector(v []float32) ([]byte, error) {
	if len(v) != embedding.Dimension {
		return nil, errors.New("wrong vector dimension")
	}
	b := make([]byte, len(v)*4)
	for i, f := range v {
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return nil, errors.New("non-finite vector")
		}
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b, nil
}
func decodeVector(b []byte) ([]float32, error) {
	if len(b) != embedding.Dimension*4 {
		return nil, errors.New("corrupt vector size")
	}
	v := make([]float32, embedding.Dimension)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
		if math.IsNaN(float64(v[i])) || math.IsInf(float64(v[i]), 0) {
			return nil, errors.New("corrupt vector")
		}
	}
	return v, nil
}

type vectorChunk struct {
	record  string
	ordinal int
	text    string
	hash    string
	data    []byte
}

func chunksFor(r Record) []vectorChunk {
	text := []rune(recordText(r))
	out := []vectorChunk{}
	for start := 0; start < len(text); start += 400 {
		end := min(start+480, len(text))
		fragment := string(text[start:end])
		value := r.Title + "\n" + fragment
		out = append(out, vectorChunk{record: r.ID, ordinal: len(out), text: fragment, hash: Hash(value)})
		if end == len(text) {
			break
		}
	}
	return out
}

func (m *Manager) IndexVectors(ctx context.Context, p Principal, project string) (VectorIndexReport, error) {
	s, err := m.Store(p, project, "write")
	if err != nil {
		return VectorIndexReport{}, err
	}
	return m.indexStoreVectors(ctx, s)
}
func (m *Manager) indexStoreVectors(ctx context.Context, s *Store) (VectorIndexReport, error) {
	started := time.Now()
	s.vectorMu.Lock()
	defer s.vectorMu.Unlock()
	status, err := s.VectorStatus(ctx)
	report := VectorIndexReport{VectorStatus: status}
	if err != nil {
		return report, err
	}
	if !status.Stale {
		report.Reused = status.Chunks
		report.DurationMS = float64(time.Since(started).Microseconds()) / 1000
		return report, nil
	}
	engine, err := m.embedder()
	if err != nil {
		return report, err
	}
	records, err := s.Knowledge(ctx, 10000)
	if err != nil {
		return report, err
	}
	// Do not mark an index complete after silently hitting a record/chunk cap.
	var count int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM records r WHERE r.deleted=0 AND r.kind IN ('note','code','symbol','fact','decision','task') AND "+headPredicate).Scan(&count); err != nil {
		return report, err
	}
	if count > len(records) {
		return report, errors.New("vector indexing exceeds 10000 live records; split the project scope")
	}
	chunks := []vectorChunk{}
	titles := map[string]string{}
	for _, r := range records {
		switch r.Kind {
		case "note", "code", "symbol", "fact", "decision", "task":
		default:
			continue
		}
		titles[r.ID] = r.Title
		next := chunksFor(r)
		if len(chunks)+len(next) > maxVectorChunks {
			return report, errors.New("vector index exceeds 40000 chunks; split the project scope")
		}
		chunks = append(chunks, next...)
	}
	cache := map[string][]byte{}
	rows, err := s.db.QueryContext(ctx, "SELECT hash,vector FROM embedding_cache WHERE model=?", status.Model)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var h string
		var data []byte
		if err = rows.Scan(&h, &data); err != nil {
			rows.Close()
			return report, err
		}
		cache[h] = data
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	missing := []int{}
	pending := map[string]bool{}
	for i := range chunks {
		if data, ok := cache[chunks[i].hash]; ok {
			chunks[i].data = data
			report.Reused++
		} else if !pending[chunks[i].hash] {
			missing = append(missing, i)
			pending[chunks[i].hash] = true
		}
	}
	// Dynamic INT8 activation ranges depend on every sequence in a batch.
	// Single-passage inference keeps content-addressed vectors independent of
	// neighbouring documents and prevents incremental reindex drift.
	for start := 0; start < len(missing); start++ {
		end := start + 1
		batch := []string{}
		for _, i := range missing[start:end] {
			batch = append(batch, titles[chunks[i].record]+"\n"+chunks[i].text)
		}
		vectors, e := engine.Embed(ctx, batch, false)
		if e != nil {
			return report, e
		}
		for j, i := range missing[start:end] {
			data, e := encodeVector(vectors[j])
			if e != nil {
				return report, e
			}
			cache[chunks[i].hash] = data
			report.Embedded++
			if _, e = s.db.ExecContext(ctx, "INSERT INTO embedding_cache(model,hash,vector,created) VALUES(?,?,?,?) ON CONFLICT(model,hash) DO UPDATE SET vector=excluded.vector,created=excluded.created", status.Model, chunks[i].hash, data, Now()); e != nil {
				return report, e
			}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM vectors WHERE model=?", status.Model); err != nil {
		return report, err
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO vectors(model,record,chunk,text,vector) VALUES(?,?,?,?,?)")
	if err != nil {
		return report, err
	}
	defer stmt.Close()
	for _, chunk := range chunks {
		if _, err = stmt.ExecContext(ctx, status.Model, chunk.record, chunk.ordinal, chunk.text, cache[chunk.hash]); err != nil {
			return report, err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO vector_state(model,revision) VALUES(?,?) ON CONFLICT(model) DO UPDATE SET revision=excluded.revision", status.Model, status.SourceRevision); err != nil {
		return report, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM embedding_cache WHERE rowid IN (SELECT rowid FROM embedding_cache ORDER BY created DESC LIMIT -1 OFFSET 40000)"); err != nil {
		return report, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM cache"); err != nil {
		return report, err
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	report.VectorStatus, err = s.VectorStatus(ctx)
	report.DurationMS = float64(time.Since(started).Microseconds()) / 1000
	return report, err
}

func (m *Manager) queryVector(ctx context.Context, s *Store, query string) ([]float32, error) {
	key := "query-vector:" + Hash(vectorIdentity()+Redact(query))
	if raw, ok, err := s.cacheGet(ctx, key, 0); err != nil {
		return nil, err
	} else if ok {
		var v []float32
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		return v, nil
	}
	engine, err := m.embedder()
	if err != nil {
		return nil, err
	}
	vs, err := engine.Embed(ctx, []string{Redact(query)}, true)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(vs[0])
	if err != nil {
		return nil, err
	}
	if err = s.cachePut(ctx, key, 0, string(b)); err != nil {
		return nil, err
	}
	return vs[0], nil
}

func (s *Store) denseSearchStream(ctx context.Context, query []float32, limit int) ([]Hit, error) {
	if err := s.ensureVectors(); err != nil {
		return nil, err
	}
	if len(query) != embedding.Dimension {
		return nil, errors.New("query dimension mismatch")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT v.record,v.text,v.vector FROM vectors v JOIN records r ON r.id=v.record WHERE v.model=? AND r.deleted=0 AND "+headPredicate, vectorIdentity())
	if err != nil {
		return nil, err
	}
	type scored struct {
		id, text string
		score    float64
	}
	best := map[string]scored{}
	for rows.Next() {
		var id, fragment string
		var raw []byte
		if err = rows.Scan(&id, &fragment, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if len(raw) != embedding.Dimension*4 {
			rows.Close()
			return nil, errors.New("corrupt vector size")
		}
		var dot float64
		for i, q := range query {
			dot += float64(q) * float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:])))
		}
		if math.IsNaN(dot) || math.IsInf(dot, 0) {
			rows.Close()
			return nil, errors.New("non-finite stored vector")
		}
		if dot >= 0.78 && dot > best[id].score {
			best[id] = scored{id, fragment, dot}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	ordered := make([]scored, 0, len(best))
	for _, v := range best {
		ordered = append(ordered, v)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].score == ordered[j].score {
			return ordered[i].id < ordered[j].id
		}
		return ordered[i].score > ordered[j].score
	})
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	out := []Hit{}
	for _, v := range ordered {
		r, e := s.Get(ctx, v.id)
		if e != nil {
			return nil, e
		}
		out = append(out, Hit{ProjectID: s.Project.ID, ID: r.ID, Entity: r.Entity, Kind: r.Kind, Title: r.Title, Snippet: v.text, Score: v.score, Created: r.Created, Dense: true})
	}
	return out, nil
}

func (m *Manager) VectorSearch(ctx context.Context, p Principal, project, query string, shared bool, limit int) ([]Hit, error) {
	limit = max(1, min(100, limit))
	if strings.TrimSpace(query) == "" || len(query) > 10000 {
		return nil, errors.New("vector query must be nonempty and at most 10000 bytes")
	}
	stores, err := m.Scopes(p, project, shared)
	if err != nil {
		return nil, err
	}
	vector, err := m.queryVector(ctx, stores[0], query)
	if err != nil {
		return nil, err
	}
	out := []Hit{}
	for _, s := range stores {
		hits, e := s.denseSearch(ctx, vector, limit)
		if e != nil {
			return nil, e
		}
		out = append(out, hits...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Manager) retrievalKey(ctx context.Context, stores []*Store) (string, error) {
	if _, err := embedding.Info(m.Root); err != nil {
		if os.IsNotExist(err) {
			return "lexical", nil
		}
		return "lexical-unavailable", nil
	}
	key := vectorIdentity()
	for _, s := range stores {
		if e := s.ensureVectors(); e != nil {
			return "", e
		}
		// Cache identity needs the indexed revision, not full vector/cache size
		// aggregation. Keep context cache hits independent of corpus byte size.
		var revision int64
		if e := s.db.QueryRowContext(ctx, "SELECT coalesce((SELECT revision FROM vector_state WHERE model=?),-1)", vectorIdentity()).Scan(&revision); e != nil {
			return "", e
		}
		key += fmt.Sprintf("/%s:%d", s.Project.ID, revision)
	}
	return key, nil
}

func fuseHits(lexical, dense []Hit, limit int) []Hit {
	items := map[string]Hit{}
	scores := map[string]float64{}
	for i, h := range lexical {
		key := h.ProjectID + "/" + h.ID
		items[key] = h
		scores[key] += 2 / float64(60+i+1)
	}
	for i, h := range dense {
		key := h.ProjectID + "/" + h.ID
		if _, exists := items[key]; !exists {
			items[key] = h
		}
		scores[key] += 1 / float64(60+i+1)
	}
	out := make([]Hit, 0, len(items))
	for key, h := range items {
		h.Score = scores[key] * 20
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].ProjectID+out[i].ID < out[j].ProjectID+out[j].ID
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
