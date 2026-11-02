package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// fakeReranker records every call and returns scripted scores or an error.
type fakeReranker struct {
	scores   func(passages []string) []float32
	err      error
	calls    int
	passages []string
}

func (f *fakeReranker) Rerank(_ context.Context, _ string, passages []string) ([]float32, error) {
	f.calls++
	f.passages = append([]string(nil), passages...)
	if f.err != nil {
		return nil, f.err
	}
	return f.scores(passages), nil
}

// rrfHits returns n hits in RRF order: ChunkID i+1 is RRF rank i+1, content "p<i+1>".
func rrfHits(n int) []SearchHit {
	hits := make([]SearchHit, n)
	for i := range hits {
		hits[i] = SearchHit{ChunkID: int64(i + 1), ArxivID: fmt.Sprintf("2401.%05d", i+1), Title: "t",
			Content: fmt.Sprintf("p%d", i+1), RRFScore: 1.0 / float64(61+i)}
	}
	return hits
}

// newEngine wires a fake reranker and a fake fetch that returns min(n, available) RRF hits and records n.
func newEngine(rr *fakeReranker, available int, gotN *int) *SearchEngine {
	return &SearchEngine{rr: rr, fetch: func(_ context.Context, _ string, _ []string, n int) ([]SearchHit, error) {
		*gotN = n
		return rrfHits(min(n, available)), nil
	}}
}

// byRank scores the passage at RRF rank r (1-based) with fn(r).
func byRank(fn func(r int) float32) func([]string) []float32 {
	return func(p []string) []float32 {
		out := make([]float32, len(p))
		for i := range p {
			out[i] = fn(i + 1)
		}
		return out
	}
}

func ids(hits []SearchHit) []int64 {
	out := make([]int64, len(hits))
	for i, h := range hits {
		out[i] = h.ChunkID
	}
	return out
}

type orderCase struct {
	name   string
	limit  int
	scores func(r int) float32
	want   []int64
}

func runOrderCases(t *testing.T, cases []orderCase) {
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var n int
			hits, err := newEngine(&fakeReranker{scores: byRank(tc.scores)}, 100, &n).
				HybridSearch(context.Background(), "q", nil, tc.limit, true)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := ids(hits); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("order = %v, want %v", got, tc.want)
			}
			for i := 1; i < len(hits); i++ {
				if hits[i-1].RerankScore < hits[i].RerankScore {
					t.Fatalf("not sorted by rerank score desc at %d", i)
				}
			}
		})
	}
}

// AC1
func TestHybridSearchRerankOrdersByScore(t *testing.T) {
	runOrderCases(t, []orderCase{
		{"rrf rank 8 has top score", 5, func(r int) float32 {
			if r == 8 {
				return 0.99
			}
			return 0.5 - float32(r)*0.01
		}, []int64{8, 1, 2, 3, 4}},
		{"fully reversed scores", 5, func(r int) float32 { return float32(r) }, []int64{20, 19, 18, 17, 16}},
		{"already sorted scores", 5, func(r int) float32 { return 1 - float32(r)*0.01 }, []int64{1, 2, 3, 4, 5}},
	})
}

// AC4
func TestHybridSearchRerankTiesKeepRRFOrder(t *testing.T) {
	tiers := map[int]float32{2: 0.9, 5: 0.9, 3: 0.8, 7: 0.8, 1: 0.7} // all other ranks 0.1
	runOrderCases(t, []orderCase{
		{"all equal", 5, func(int) float32 { return 0.5 }, []int64{1, 2, 3, 4, 5}},
		{"tied pairs interleaved", 6, func(r int) float32 {
			if s, ok := tiers[r]; ok {
				return s
			}
			return 0.1
		}, []int64{2, 5, 3, 7, 1, 4}},
	})
}

// AC2
func TestHybridSearchRerankPoolSize(t *testing.T) {
	cases := []struct {
		name                                         string
		limit, available, wantN, wantCalls, wantPass int
		wantLen                                      int
	}{
		{"limit 5 reranks pool of 20", 5, 100, 20, 1, 20, 5},
		{"limit 20", 20, 100, 20, 1, 20, 20},
		{"limit 50 above pool", 50, 100, 50, 1, 50, 50},
		{"limit 0 defaults to 20", 0, 100, 20, 1, 20, 20},
		{"only 7 candidates", 5, 7, 20, 1, 7, 5},
		{"no candidates no call", 5, 0, 20, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var n int
			rr := &fakeReranker{scores: byRank(func(r int) float32 { return 1 / float32(r) })}
			hits, err := newEngine(rr, tc.available, &n).HybridSearch(context.Background(), "q", nil, tc.limit, true)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := []int{n, rr.calls, len(rr.passages), len(hits)}
			if want := []int{tc.wantN, tc.wantCalls, tc.wantPass, tc.wantLen}; !reflect.DeepEqual(got, want) {
				t.Fatalf("[fetch n, rerank calls, passages, len(hits)] = %v, want %v", got, want)
			}
		})
	}
}

// AC3
func TestHybridSearchRerankFallback(t *testing.T) {
	const traceID = "0123456789abcdef0123456789abcdef"
	sized := func(d int) func([]string) []float32 {
		return func(p []string) []float32 { return make([]float32, len(p)+d) }
	}
	nanAt3 := byRank(func(r int) float32 {
		if r == 3 {
			return float32(math.NaN())
		}
		return float32(r)
	})
	cases := []struct {
		name       string
		err        error
		scores     func([]string) []float32
		wantReason string
	}{
		{"rerank error", errors.New(`status 422: {"input":"secret passage"}`), nil, `reason="rerank error"`},
		{"timeout", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), nil, `reason="rerank timeout"`},
		{"too few scores", nil, sized(-1), `reason="score count mismatch"`},
		{"too many scores", nil, sized(+1), `reason="score count mismatch"`},
		{"NaN score", nil, nanAt3, `reason="NaN score"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			prevOut, prevFlags := log.Writer(), log.Flags()
			log.SetOutput(&buf)
			log.SetFlags(0)
			t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })

			var n int
			ctx := withTraceID(context.Background(), traceID)
			hits, err := newEngine(&fakeReranker{scores: tc.scores, err: tc.err}, 100, &n).
				HybridSearch(ctx, "secret query", nil, 5, true)
			if err != nil {
				t.Fatalf("error leaked to caller: %v", err)
			}
			if got := ids(hits); !reflect.DeepEqual(got, []int64{1, 2, 3, 4, 5}) {
				t.Fatalf("order = %v, want RRF top 5", got)
			}
			for _, h := range hits {
				if h.RerankScore != 0 {
					t.Fatalf("RerankScore = %v on fallback, want 0", h.RerankScore)
				}
			}
			line := buf.String()
			for _, want := range []string{"rerank fallback", "trace_id=" + traceID + " ", "trace_id_source=ctx", tc.wantReason, "pool=20"} {
				if !strings.Contains(line, want) {
					t.Errorf("log %q missing %q", line, want)
				}
			}
			for _, leak := range []string{"secret query", "secret passage"} {
				if strings.Contains(line, leak) {
					t.Errorf("log %q leaks content %q", line, leak)
				}
			}
		})
	}
}

func TestTraceIDFrom(t *testing.T) {
	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	cases := []struct {
		name            string
		ctx             context.Context
		wantID, wantSrc string // wantID "" means: a generated 32-hex ID
	}{
		{"from ctx", withTraceID(context.Background(), "abc"), "abc", "ctx"},
		{"empty ctx value", withTraceID(context.Background(), ""), "", "generated"},
		{"no value", context.Background(), "", "generated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, src := traceIDFrom(tc.ctx)
			if src != tc.wantSrc || (tc.wantID != "" && id != tc.wantID) || (tc.wantID == "" && !hex32.MatchString(id)) {
				t.Fatalf("traceIDFrom = (%q, %q), want (%q, %q)", id, src, tc.wantID, tc.wantSrc)
			}
		})
	}
}

// AC5
func TestHybridSearchNoRerankUnchanged(t *testing.T) {
	for _, tc := range []struct{ limit, wantN int }{{5, 5}, {20, 20}, {0, 20}} {
		t.Run(fmt.Sprintf("limit %d", tc.limit), func(t *testing.T) {
			var n int
			rr := &fakeReranker{scores: byRank(func(r int) float32 { return float32(r) })}
			hits, err := newEngine(rr, 100, &n).HybridSearch(context.Background(), "q", nil, tc.limit, false)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if n != tc.wantN || rr.calls != 0 {
				t.Fatalf("fetch n = %d, rerank calls = %d; want %d, 0", n, rr.calls, tc.wantN)
			}
			got, _ := json.Marshal(hits)
			want, _ := json.Marshal(rrfHits(tc.wantN))
			if !bytes.Equal(got, want) || bytes.Contains(got, []byte("rerank_score")) {
				t.Fatalf("output changed:\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestHybridSearchFetchError(t *testing.T) {
	fetchErr := errors.New("hybrid search query failed: boom")
	for _, rerank := range []bool{false, true} {
		t.Run(fmt.Sprintf("rerank=%v", rerank), func(t *testing.T) {
			rr := &fakeReranker{}
			s := &SearchEngine{rr: rr, fetch: func(context.Context, string, []string, int) ([]SearchHit, error) {
				return nil, fetchErr
			}}
			hits, err := s.HybridSearch(context.Background(), "q", nil, 5, rerank)
			if err != fetchErr || hits != nil || rr.calls != 0 {
				t.Fatalf("got (%v, %v, calls=%d), want (nil, fetch error unchanged, 0)", hits, err, rr.calls)
			}
		})
	}
}

func TestNewSearchEngineWiresSeams(t *testing.T) {
	ec := &EmbedClient{}
	s := NewSearchEngine(nil, ec, "")
	if s.rr != reranker(ec) || s.fetch == nil || s.modelName != "BAAI/bge-base-en-v1.5" {
		t.Fatalf("NewSearchEngine did not wire defaults")
	}
}
