package embedding

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"

	tokenizers "github.com/amikos-tech/pure-tokenizers"
	ort "github.com/benedoc-inc/onnxer/onnxruntime"
)

type Engine struct {
	mu               sync.Mutex
	runtime          *ort.Runtime
	env              *ort.Env
	session          *ort.Session
	tokenizer        *tokenizers.Tokenizer
	closed           bool
	ids, mask, types []int64
}

func Open(data string, threads int) (*Engine, error) {
	info, err := Verify(data)
	if err != nil {
		return nil, err
	}
	dir := Directory(data)
	e := &Engine{}
	defer func() {
		if err != nil {
			_ = e.Close()
		}
	}()
	e.runtime, err = ort.NewRuntime(filepath.Join(dir, info.Runtime), 24)
	if err != nil {
		return nil, err
	}
	e.env, err = e.runtime.NewEnv("akasha-embeddings", ort.LoggingLevelWarning)
	if err != nil {
		return nil, err
	}
	if err = e.env.DisableTelemetry(); err != nil {
		return nil, err
	}
	if threads < 1 {
		threads = 1
	}
	if threads > 8 {
		threads = 8
	}
	f, err := os.Open(filepath.Join(dir, "model.onnx"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// ORT's file API uses UTF-16 on Windows. The byte-array API avoids a
	// third-party path conversion discrepancy and supports Cyrillic paths.
	e.session, err = e.runtime.NewSessionFromReader(e.env, f, &ort.SessionOptions{IntraOpNumThreads: threads, InterOpNumThreads: 1, GraphOptimization: ort.GraphOptimizationAll, ConfigEntries: map[string]string{"session.intra_op.allow_spinning": "0", "session.inter_op.allow_spinning": "0"}})
	if err != nil {
		return nil, err
	}
	e.tokenizer, err = tokenizers.FromFile(filepath.Join(dir, "tokenizer.json"), tokenizers.WithLibraryPath(filepath.Join(dir, info.TokenizerRuntime)), tokenizers.WithTruncation(SequenceLength, tokenizers.TruncationDirectionRight, tokenizers.TruncationStrategyLongestFirst))
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	errs := []error{}
	if e.tokenizer != nil {
		errs = append(errs, e.tokenizer.Close())
	}
	if e.session != nil {
		e.session.Close()
	}
	if e.env != nil {
		e.env.Close()
	}
	if e.runtime != nil {
		errs = append(errs, e.runtime.Close())
	}
	return errors.Join(errs...)
}

// Embed uses dynamic padding, masked mean pooling and normalized vectors.
// Calls share one native session; ORT parallelizes tensor math within its
// bounded thread pool. No HTTP client or auto-download path is reachable here.
func (e *Engine) Embed(ctx context.Context, texts []string, query bool) ([][]float32, error) {
	if len(texts) < 1 || len(texts) > 16 {
		return nil, errors.New("embedding batch must contain 1..16 texts")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errors.New("embedding engine closed")
	}
	encodings := make([]*tokenizers.EncodeResult, len(texts))
	length := 0
	prefix := "passage: "
	if query {
		prefix = "query: "
	}
	for i, text := range texts {
		if len(text) > 32000 {
			return nil, errors.New("embedding text exceeds limit")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		enc, err := e.tokenizer.Encode(prefix+text, tokenizers.WithAddSpecialTokens(), tokenizers.WithReturnAttentionMask(), tokenizers.WithReturnTypeIDs())
		if err != nil {
			return nil, err
		}
		encodings[i] = enc
		if len(enc.IDs) > length {
			length = len(enc.IDs)
		}
	}
	if length < 1 || length > SequenceLength {
		return nil, errors.New("invalid tokenizer sequence length")
	}
	n := len(texts) * length
	resize := func(b []int64) []int64 {
		if cap(b) < n {
			return make([]int64, n)
		}
		b = b[:n]
		clear(b)
		return b
	}
	e.ids, e.mask, e.types = resize(e.ids), resize(e.mask), resize(e.types)
	ids, mask, types := e.ids, e.mask, e.types
	for i := range ids {
		ids[i] = 1
	} // XLM-R <pad> token.
	for i, enc := range encodings {
		for j, id := range enc.IDs {
			offset := i*length + j
			ids[offset] = int64(id)
			mask[offset] = 1
			if j < len(enc.TypeIDs) {
				types[offset] = int64(enc.TypeIDs[j])
			}
		}
	}
	inputs := map[string]*ort.Value{}
	defer func() {
		for _, v := range inputs {
			v.Close()
		}
	}()
	for _, name := range e.session.InputNames() {
		var data []int64
		switch name {
		case "input_ids":
			data = ids
		case "attention_mask":
			data = mask
		case "token_type_ids":
			data = types
		default:
			return nil, errors.New("unexpected embedding input: " + name)
		}
		value, err := ort.NewTensorValue(e.runtime, data, []int64{int64(len(texts)), int64(length)})
		if err != nil {
			return nil, err
		}
		inputs[name] = value
	}
	outputs, err := e.session.Run(ctx, inputs)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, v := range outputs {
			v.Close()
		}
	}()
	v, ok := outputs["last_hidden_state"]
	if !ok {
		return nil, errors.New("embedding output missing")
	}
	// Borrow native output until the deferred output.Close; mean pooling owns
	// only the final 384 floats, avoiding a copy of the full token tensor.
	values, shape, err := ort.GetTensorDataUnsafe[float32](v)
	if err != nil {
		return nil, err
	}
	if len(shape) != 3 || shape[0] != int64(len(texts)) || shape[1] != int64(length) || shape[2] != Dimension || len(values) != len(texts)*length*Dimension {
		return nil, errors.New("embedding shape mismatch")
	}
	result := make([][]float32, len(texts))
	for row := range result {
		vec := make([]float32, Dimension)
		count := float32(len(encodings[row].IDs))
		for col := 0; col < len(encodings[row].IDs); col++ {
			start := (row*length + col) * Dimension
			for dim := range vec {
				vec[dim] += values[start+dim] / count
			}
		}
		var norm float64
		for _, value := range vec {
			norm += float64(value) * float64(value)
		}
		norm = math.Sqrt(norm)
		if norm == 0 || math.IsNaN(norm) || math.IsInf(norm, 0) {
			return nil, errors.New("invalid embedding values")
		}
		for dim := range vec {
			vec[dim] /= float32(norm)
		}
		result[row] = vec
	}
	return result, nil
}
