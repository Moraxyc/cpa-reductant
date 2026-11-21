package engine

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/yetone/magpie/pkg/guard"
)

type fakeSpanFinder struct {
	fn func(text string) []guard.Span
}

func (f fakeSpanFinder) Find(text string) []guard.Span {
	return f.fn(text)
}

func TestMaskJSONWithModelAndRestore(t *testing.T) {
	dir := writeMarkerModelDir(t)
	cfg := &config.Config{
		Model: config.ModelConfig{
			Enabled:  true,
			Dir:      dir,
			MinScore: 0.75,
		},
	}
	eng := New(cfg)
	var calls atomic.Int32
	eng.model.loader = func(string) (spanFinder, error) {
		return fakeSpanFinder{fn: func(text string) []guard.Span {
			calls.Add(1)
			var spans []guard.Span
			if idx := strings.Index(text, "Alice Smith"); idx >= 0 {
				spans = append(spans, guard.Span{Start: idx, End: idx + len("Alice Smith"), Label: "private_person", Score: 0.92})
			}
			if idx := strings.Index(text, "Bob LowScore"); idx >= 0 {
				spans = append(spans, guard.Span{Start: idx, End: idx + len("Bob LowScore"), Label: "private_person", Score: 0.60})
			}
			return spans
		}}, nil
	}

	req := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"Contact Alice Smith and Bob LowScore immediately."}]}`)
	masked, err := eng.MaskJSON("claude", req)
	if err != nil {
		t.Fatalf("MaskJSON: %v", err)
	}
	if strings.Contains(string(masked), "Alice Smith") {
		t.Fatalf("expected Alice Smith to be masked, got %s", masked)
	}
	if !strings.Contains(string(masked), "{{PERSON_") {
		t.Fatalf("expected PERSON placeholder in %s", masked)
	}
	if !strings.Contains(string(masked), "Bob LowScore") {
		t.Fatalf("expected low-score span below 0.75 to stay unmasked, got %s", masked)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 model call, got %d", calls.Load())
	}

	// Second call with identical text must hit the SHA-256 LRU cache.
	if _, err := eng.MaskJSON("claude", req); err != nil {
		t.Fatalf("second MaskJSON: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected cache hit on second call, got %d model calls", calls.Load())
	}

	// RestoreJSON should recover original person name.
	var payload map[string]any
	_ = json.Unmarshal(masked, &payload)
	placeholderMsg := payload["messages"].([]any)[0].(map[string]any)["content"].(string)
	resp := []byte(fmt.Sprintf(`{"content":[{"type":"text","text":%q}]}`, placeholderMsg))
	restored := eng.RestoreJSON(resp)
	if !strings.Contains(string(restored), "Alice Smith") {
		t.Fatalf("expected restored response to contain Alice Smith, got %s", restored)
	}
}

func TestModelStrictAndDegradeAndPanicSafety(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "nonexistent")

	// Non-strict mode degrades gracefully to rules when model files are missing.
	engLenient := New(&config.Config{
		Words: []string{"LOCAL-WORD"},
		Model: config.ModelConfig{
			Enabled: true,
			Dir:     missingDir,
			Strict:  false,
		},
	})
	req := []byte(`{"messages":[{"role":"user","content":"secret word LOCAL-WORD for Alice Smith"}]}`)
	masked, err := engLenient.MaskJSON("openai", req)
	if err != nil {
		t.Fatalf("lenient MaskJSON should degrade, got err=%v", err)
	}
	if strings.Contains(string(masked), "LOCAL-WORD") {
		t.Fatalf("expected rules/words to still mask in degraded mode, got %s", masked)
	}

	// Strict mode fails closed with ErrModelUnavailable when files are missing.
	engStrict := New(&config.Config{
		Model: config.ModelConfig{
			Enabled: true,
			Dir:     missingDir,
			Strict:  true,
		},
	})
	if _, err := engStrict.MaskJSON("openai", req); !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("expected ErrModelUnavailable in strict mode, got %v", err)
	}

	// Panics inside Find must never escape MaskJSONContext.
	dir := writeMarkerModelDir(t)
	engPanic := New(&config.Config{
		Model: config.ModelConfig{
			Enabled: true,
			Dir:     dir,
			Strict:  true,
		},
	})
	engPanic.model.loader = func(string) (spanFinder, error) {
		return fakeSpanFinder{fn: func(string) []guard.Span {
			panic("boom inside model")
		}}, nil
	}
	if _, err := engPanic.MaskJSONContext(context.Background(), "openai", req); !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("expected recovered ErrModelUnavailable on panic, got %v", err)
	}
}

func TestModelTimeoutAndBackgroundCacheFill(t *testing.T) {
	dir := writeMarkerModelDir(t)
	eng := New(&config.Config{
		Words: []string{"RULE-TOKEN"},
		Model: config.ModelConfig{
			Enabled: true,
			Dir:     dir,
			Strict:  false,
			WaitMS:  30,
		},
	})
	release := make(chan struct{})
	var calls atomic.Int32
	eng.model.loader = func(string) (spanFinder, error) {
		return fakeSpanFinder{fn: func(text string) []guard.Span {
			calls.Add(1)
			<-release
			if idx := strings.Index(text, "Alice Smith"); idx >= 0 {
				return []guard.Span{{Start: idx, End: idx + len("Alice Smith"), Label: "private_person", Score: 0.95}}
			}
			return nil
		}}, nil
	}

	req := []byte(`{"messages":[{"role":"user","content":"RULE-TOKEN belongs to Alice Smith"}]}`)
	t0 := time.Now()
	masked, err := eng.MaskJSON("openai", req)
	elapsed := time.Since(t0)
	if err != nil {
		t.Fatalf("timeout should degrade in non-strict mode, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected fast timeout fallback, took %v", elapsed)
	}
	if strings.Contains(string(masked), "RULE-TOKEN") {
		t.Fatalf("expected rules to still mask on timeout, got %s", masked)
	}

	// Release the in-flight piece so it populates the cache; next call must see Alice Smith masked immediately.
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		out, err := eng.MaskJSON("openai", req)
		if err == nil && !strings.Contains(string(out), "Alice Smith") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected subsequent call to hit background-populated cache and mask Alice Smith")
}

func TestModelChunkingAndInflightDedup(t *testing.T) {
	dir := writeMarkerModelDir(t)
	eng := New(&config.Config{
		Model: config.ModelConfig{
			Enabled: true,
			Dir:     dir,
			WaitMS:  5000,
		},
	})
	var calls atomic.Int32
	gate := make(chan struct{})
	eng.model.loader = func(string) (spanFinder, error) {
		return fakeSpanFinder{fn: func(text string) []guard.Span {
			calls.Add(1)
			<-gate
			var spans []guard.Span
			if idx := strings.Index(text, "Alice Smith"); idx >= 0 {
				spans = append(spans, guard.Span{Start: idx, End: idx + len("Alice Smith"), Label: "private_person", Score: 0.95})
			}
			return spans
		}}, nil
	}

	// Identical concurrent requests share one inference per chunk.
	req := []byte(`{"messages":[{"role":"user","content":"Hello Alice Smith"}]}`)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := eng.MaskJSON("openai", req)
			if err != nil || strings.Contains(string(out), "Alice Smith") {
				t.Errorf("concurrent MaskJSON failed: err=%v out=%s", err, out)
			}
		}()
	}
	time.Sleep(30 * time.Millisecond)
	close(gate)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 deduplicated inference call, got %d", got)
	}

	// Long text > 4KB gets split into overlapping chunks and spreadFound masks repeats.
	longText := "Alice Smith started here.\n" + strings.Repeat("ordinary engineering log line\n", 220) + "Later Alice Smith logged out.\n"
	longReq, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{{"role": "user", "content": longText}},
	})
	out, err := eng.MaskJSON("openai", longReq)
	if err != nil {
		t.Fatalf("long MaskJSON: %v", err)
	}
	if strings.Contains(string(out), "Alice Smith") {
		t.Fatalf("expected all occurrences of Alice Smith across >4KB chunks to be masked")
	}
}

func TestGuardLoadSyntheticONNXEndToEnd(t *testing.T) {
	dir := writeSyntheticGuardModel(t)
	m, err := guard.Load(dir)
	if err != nil {
		t.Fatalf("guard.Load(synthetic): %v", err)
	}
	spans := m.Find("Hello Alice Smith from Beijing")
	_ = spans // Synthetic zero weights produce low/uniform logits; verifying Load+Find runs end-to-end without error.
}

func BenchmarkGuardRealBERTForward4KB(b *testing.B) {
	dir := writeSyntheticGuardModel(b)
	m, err := guard.Load(dir)
	if err != nil {
		b.Fatalf("guard.Load(synthetic): %v", err)
	}
	// Repeat a known vocab token so 510 tokens = 1 full BERT window and 1200 tokens = 3 windows (~4 KB).
	oneWindow := strings.Repeat("tok ", 500)
	threeWindows := strings.Repeat("tok ", 1200)

	b.Run("1_window_500tok", func(b *testing.B) {
		b.SetBytes(int64(len(oneWindow)))
		for range b.N {
			_ = m.Find(oneWindow)
		}
	})
	b.Run("3_windows_1200tok_4KB_equiv", func(b *testing.B) {
		b.SetBytes(int64(len(threeWindows)))
		for range b.N {
			_ = m.Find(threeWindows)
		}
	})
}

func writeMarkerModelDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	for _, rel := range guard.Files {
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(rel)), []byte("stub"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// writeSyntheticGuardModel writes a valid minimal 40003-vocab tokenizer.json and
// 12-layer quantized BERT ONNX model matching Magpie's internal/pii/guard loader.
func writeSyntheticGuardModel(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()

	vocab := make([][2]any, 40003)
	vocab[0] = [2]any{"<s>", 0.0}
	vocab[1] = [2]any{"<pad>", 0.0}
	vocab[2] = [2]any{"</s>", 0.0}
	vocab[3] = [2]any{"<unk>", 0.0}
	vocab[4] = [2]any{"▁", -1.0}
	vocab[5] = [2]any{"▁tok", -0.5}
	vocab[6] = [2]any{"tok", -0.5}
	for i := 7; i < len(vocab); i++ {
		vocab[i] = [2]any{fmt.Sprintf("w%d", i), -5.0}
	}
	charmap := base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 0})
	tokPayload, _ := json.Marshal(map[string]any{
		"added_tokens": []map[string]any{
			{"id": 0, "content": "<s>", "special": true},
			{"id": 2, "content": "</s>", "special": true},
		},
		"normalizer": map[string]any{
			"type":                 "Precompiled",
			"precompiled_charsmap": charmap,
		},
		"pre_tokenizer": map[string]any{
			"pretokenizers": []map[string]any{{"type": "Metaspace"}},
		},
		"model": map[string]any{
			"type":   "Unigram",
			"unk_id": 3,
			"vocab":  vocab,
		},
	})
	if err := os.WriteFile(filepath.Join(dir, "tokenizer.json"), tokPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "model_int8.onnx_data"), []byte("inline"), 0o600); err != nil {
		t.Fatal(err)
	}

	var graph []byte
	addTensor := func(name string, dtype int, dims []int64, elemBytes int) {
		n := int64(1)
		for _, d := range dims {
			n *= d
		}
		raw := make([]byte, int(n)*elemBytes)
		var tp []byte
		for _, d := range dims {
			tp = appendVarintField(tp, 1, uint64(d))
		}
		tp = appendVarintField(tp, 2, uint64(dtype))
		tp = appendBytesField(tp, 8, []byte(name))
		tp = appendBytesField(tp, 9, raw)
		graph = appendBytesField(graph, 5, tp)
	}
	addFloat := func(name string, dims ...int64) { addTensor(name, 1, dims, 4) }
	addUint8 := func(name string, dims ...int64) { addTensor(name, 2, dims, 1) }
	addInt8 := func(name string, dims ...int64) { addTensor(name, 3, dims, 1) }
	addInt64 := func(name string, dims ...int64) {
		n := int64(1)
		for _, d := range dims {
			n *= d
		}
		raw := make([]byte, int(n)*8)
		for i := int64(0); i < n; i++ {
			binary.LittleEndian.PutUint64(raw[i*8:], uint64(i%512))
		}
		var tp []byte
		for _, d := range dims {
			tp = appendVarintField(tp, 1, uint64(d))
		}
		tp = appendVarintField(tp, 2, 7)
		tp = appendBytesField(tp, 8, []byte(name))
		tp = appendBytesField(tp, 9, raw)
		graph = appendBytesField(graph, 5, tp)
	}
	addQMat := func(prefix string, k, n int64) {
		addInt8(prefix+"_quantized", k, n)
		addFloat(prefix+"_scale", n)
		addInt8(prefix+"_zero_point", n)
	}

	addUint8("inner.bert.embeddings.word_embeddings.weight_quantized", 40003, 384)
	addFloat("inner.bert.embeddings.word_embeddings.weight_scale", 384)
	addUint8("inner.bert.embeddings.word_embeddings.weight_zero_point", 384)
	addUint8("inner.bert.embeddings.position_embeddings.weight_quantized", 512, 384)
	addFloat("inner.bert.embeddings.position_embeddings.weight_scale", 384)
	addUint8("inner.bert.embeddings.position_embeddings.weight_zero_point", 384)
	addUint8("inner.bert.embeddings.token_type_embeddings.weight_quantized", 2, 384)
	addFloat("inner.bert.embeddings.token_type_embeddings.weight_scale", 384)
	addUint8("inner.bert.embeddings.token_type_embeddings.weight_zero_point", 384)
	addInt64("onnx::Slice_206", 1, 512)
	addFloat("inner.bert.embeddings.LayerNorm.weight", 384)
	addFloat("inner.bert.embeddings.LayerNorm.bias", 384)

	for i := range 12 {
		p := fmt.Sprintf("inner.bert.encoder.layer.%d.", i)
		m := 1299 + 16*i
		addQMat(fmt.Sprintf("Attention_%d_qkv_weight", i), 384, 3*384)
		addFloat(fmt.Sprintf("Attention_%d_qkv_bias", i), 3*384)
		addQMat(fmt.Sprintf("onnx::MatMul_%d", m), 384, 384)
		addFloat(p+"attention.output.dense.bias", 384)
		addFloat(p+"attention.output.LayerNorm.weight", 384)
		addFloat(p+"attention.output.LayerNorm.bias", 384)
		addQMat(fmt.Sprintf("onnx::MatMul_%d", m+1), 384, 1536)
		addFloat(p+"intermediate.dense.bias", 1536)
		addFloat(fmt.Sprintf("r15_smooth_inv_s_%d", i), 1536)
		addQMat(fmt.Sprintf("onnx::MatMul_%d", m+2), 1536, 384)
		addFloat(p+"output.dense.bias", 384)
		addFloat(p+"output.LayerNorm.weight", 384)
		addFloat(p+"output.LayerNorm.bias", 384)
	}
	addQMat("onnx::MatMul_1478", 384, 21)
	addFloat("inner.classifier.bias", 21)

	modelProto := appendBytesField(nil, 7, graph)
	if err := os.WriteFile(filepath.Join(dir, "model_int8.onnx"), modelProto, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func appendVarintField(dst []byte, fieldNum int, val uint64) []byte {
	dst = binary.AppendUvarint(dst, uint64(fieldNum<<3|0))
	return binary.AppendUvarint(dst, val)
}

func appendBytesField(dst []byte, fieldNum int, b []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(fieldNum<<3|2))
	dst = binary.AppendUvarint(dst, uint64(len(b)))
	return append(dst, b...)
}
