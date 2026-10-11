package core

import (
	"akasha/internal/embedding"
	"context"
	"fmt"
	"math"
	"testing"
)

func BenchmarkVectorScan1000(b *testing.B) {
	s, e := OpenStore(b.TempDir()+"/bench.db", Project{ID: "bench", SpaceID: "bench"})
	if e != nil {
		b.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	if e = s.ensureVectors(); e != nil {
		b.Fatal(e)
	}
	v := make([]float32, embedding.Dimension)
	for i := range v {
		v[i] = float32(1 / math.Sqrt(embedding.Dimension))
	}
	blob, _ := encodeVector(v)
	for i := 0; i < 1000; i++ {
		r, _, err := s.Put(ctx, "bench", WriteInput{Kind: "note", Entity: fmt.Sprint(i), Title: "Fixture", Text: "A document for the bounded vector scan benchmark"})
		if err != nil {
			b.Fatal(err)
		}
		if _, err = s.db.Exec("INSERT INTO vectors VALUES(?,?,?,?,?)", vectorIdentity(), r.ID, 0, r.Text, blob); err != nil {
			b.Fatal(err)
		}
	}
	if _, e = s.db.Exec("INSERT INTO vector_state VALUES(?,1000)", vectorIdentity()); e != nil {
		b.Fatal(e)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.denseSearch(ctx, v, 5); err != nil {
			b.Fatal(err)
		}
	}
}
