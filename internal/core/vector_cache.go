package core

import (
	"akasha/internal/cache"
	"akasha/internal/embedding"
	"akasha/internal/resources"
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
)

type vectorRow struct{ id, text, entity, kind, title, created string }
type vectorMatrix struct {
	rows   []vectorRow
	values []float32
}

var residentVectors = cache.New[*vectorMatrix](resources.Default().VectorCacheBytes)

func VectorCacheStats() cache.Stats { return residentVectors.Stats() }
func ClearResidentVectors()         { residentVectors.Clear() }

func (s *Store) vectorMatrix(ctx context.Context) (*vectorMatrix, error) {
	if err := s.ensureVectors(); err != nil {
		return nil, err
	}
	var source, indexed int64
	err := s.db.QueryRowContext(ctx, "SELECT (SELECT coalesce(max(seq),0) FROM records WHERE kind IN ('note','code','symbol','fact','decision','task')),(SELECT coalesce((SELECT revision FROM vector_state WHERE model=?),-1))", vectorIdentity()).Scan(&source, &indexed)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s/%s/%s/%d/%d", s.Path, s.Project.ID, vectorIdentity(), source, indexed)
	if matrix, ok := residentVectors.Get(key); ok {
		return matrix, nil
	}
	s.vectorLoadMu.Lock()
	defer s.vectorLoadMu.Unlock()
	if matrix, ok := residentVectors.Get(key); ok {
		return matrix, nil
	}
	budget := residentVectors.Stats().Budget
	status, err := s.VectorStatus(ctx)
	if err != nil {
		return nil, err
	}
	if status.VectorBytes > budget {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, "SELECT v.record,v.text,v.vector,r.entity,r.kind,r.title,r.created FROM vectors v JOIN records r ON r.id=v.record WHERE v.model=? AND r.deleted=0 AND "+headPredicate, vectorIdentity())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	matrix := &vectorMatrix{rows: make([]vectorRow, 0, status.Chunks), values: make([]float32, 0, status.Chunks*embedding.Dimension)}
	used := int64(cap(matrix.values)*4 + cap(matrix.rows)*96)
	for rows.Next() {
		var r vectorRow
		var raw []byte
		if err = rows.Scan(&r.id, &r.text, &raw, &r.entity, &r.kind, &r.title, &r.created); err != nil {
			return nil, err
		}
		v, e := decodeVector(raw)
		if e != nil {
			return nil, e
		}
		used += int64(len(r.id) + len(r.text) + len(r.entity) + len(r.kind) + len(r.title) + len(r.created))
		if used > budget {
			return nil, nil
		}
		matrix.rows = append(matrix.rows, r)
		matrix.values = append(matrix.values, v...)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	residentVectors.Put(key, matrix, used)
	return matrix, nil
}

func (s *Store) denseSearch(ctx context.Context, query []float32, limit int) ([]Hit, error) {
	if len(query) != embedding.Dimension {
		return nil, errors.New("query dimension mismatch")
	}
	for _, q := range query {
		if math.IsNaN(float64(q)) || math.IsInf(float64(q), 0) {
			return nil, errors.New("non-finite query")
		}
	}
	matrix, err := s.vectorMatrix(ctx)
	if err != nil {
		return nil, err
	}
	if matrix == nil {
		return s.denseSearchStream(ctx, query, limit)
	}
	// Contiguous normalized float32 vectors avoid decoding and normalizing on
	// every query. Large scans use bounded disjoint ranges; small scans stay inline.
	scores := make([]float64, len(matrix.rows))
	workers := 1
	if len(scores) >= 4096 {
		workers = resources.Default().Workers
	}
	scan := func(part int) {
		from, to := len(scores)*part/workers, len(scores)*(part+1)/workers
		for row := from; row < to; row++ {
			var dot float64
			v := matrix.values[row*embedding.Dimension : (row+1)*embedding.Dimension]
			for i, q := range query {
				dot += float64(q) * float64(v[i])
			}
			scores[row] = dot
		}
	}
	if workers == 1 {
		scan(0)
	} else {
		resources.Parallel(ctx, workers, workers, scan)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	best := map[string]int{}
	for i, r := range matrix.rows {
		if scores[i] < 0.78 {
			continue
		}
		old, ok := best[r.id]
		if !ok || scores[i] > scores[old] {
			best[r.id] = i
		}
	}
	ordered := make([]int, 0, len(best))
	for _, i := range best {
		ordered = append(ordered, i)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if scores[a] == scores[b] {
			return matrix.rows[a].id < matrix.rows[b].id
		}
		return scores[a] > scores[b]
	})
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	out := make([]Hit, 0, len(ordered))
	for _, i := range ordered {
		r := matrix.rows[i]
		out = append(out, Hit{ProjectID: s.Project.ID, ID: r.id, Entity: r.entity, Kind: r.kind, Title: r.title, Snippet: r.text, Score: scores[i], Created: r.created, Dense: true})
	}
	return out, nil
}
