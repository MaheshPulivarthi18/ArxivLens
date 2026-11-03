package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// fakeSearcher returns canned hits and records the arguments Ask passed.
type fakeSearcher struct {
	hits      []SearchHit
	err       error
	gotLimit  int
	gotRerank bool
}

func (f *fakeSearcher) HybridSearch(_ context.Context, _ string, _ []string, limit int, rerank bool) ([]SearchHit, error) {
	f.gotLimit, f.gotRerank = limit, rerank
	return f.hits, f.err
}

// fakeEmbedder maps each text to a vector via vec (nil = no vector returned) and records the texts.
// err fails every call; failOn (optional) fails only calls containing a matching text.
type fakeEmbedder struct {
	vec    func(text string) []float32
	err    error
	failOn func(text string) bool
	texts  []string
}

func (f *fakeEmbedder) EmbedTexts(_ context.Context, texts []string, _ bool) ([][]float32, error) {
	f.texts = append(f.texts, texts...)
	if f.err != nil {
		return nil, f.err
	}
	for _, t := range texts {
		if f.failOn != nil && f.failOn(t) {
			return nil, errors.New("embed failed")
		}
	}
	var out [][]float32
	for _, t := range texts {
		if v := f.vec(t); v != nil {
			out = append(out, v)
		}
	}
	return out, nil
}

// simEmbedder embeds every chunk ("chunk ...") as [1, 0] and each sentence as a unit
// vector whose cosine with [1, 0] is sim(sentence).
func simEmbedder(sim func(sentence string) float64) *fakeEmbedder {
	return &fakeEmbedder{vec: func(t string) []float32 {
		if strings.HasPrefix(t, "chunk") {
			return []float32{1, 0}
		}
		c := sim(t)
		return []float32{float32(c), float32(math.Sqrt(1 - c*c))}
	}}
}

func constSim(c float64) func(string) float64 { return func(string) float64 { return c } }

func ragHit(id int64, section string, rerank float64) SearchHit {
	return SearchHit{ChunkID: id, ArxivID: "2401.0000" + string(rune('0'+id)), Title: "Paper",
		SectionTitle: section, Content: "chunk " + section, RerankScore: rerank}
}

func markers(cs []Citation) []int {
	out := []int{}
	for _, c := range cs {
		out = append(out, c.Marker)
	}
	return out
}

func TestFilterRelevantHits(t *testing.T) {
	tests := []struct {
		name string
		hits []SearchHit
		want []int64
	}{
		{"below relevance cutoff dropped", []SearchHit{ragHit(1, "Method", 0.44)}, []int64{}},
		{"at relevance cutoff kept", []SearchHit{ragHit(1, "Method", 0.45)}, []int64{1}},
		// Current behaviour: 0 means "no rerank score" and bypasses the relevance cutoff.
		{"zero rerank score bypasses relevance cutoff", []SearchHit{ragHit(1, "Method", 0)}, []int64{1}},
		{"appendix below aux cutoff dropped", []SearchHit{ragHit(1, "Appendix A", 0.69)}, []int64{}},
		{"appendix at aux cutoff kept", []SearchHit{ragHit(1, "Appendix A", 0.70)}, []int64{1}},
		{"supplementary case-insensitive dropped", []SearchHit{ragHit(1, "SUPPLEMENTARY Material", 0.6)}, []int64{}},
		{"table section dropped", []SearchHit{ragHit(1, "Table 3: Hardware", 0.5)}, []int64{}},
		{"table section strongly relevant kept", []SearchHit{ragHit(1, "Table 3: Hardware", 0.85)}, []int64{1}},
		{"appendix with zero rerank dropped", []SearchHit{ragHit(1, "appendix", 0)}, []int64{}},
		{"mixed keeps order of survivors", []SearchHit{ragHit(1, "Intro", 0.8), ragHit(2, "Method", 0.3),
			ragHit(3, "Appendix", 0.6), ragHit(4, "Results", 0.5)}, []int64{1, 4}},
		{"empty input", nil, []int64{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := []int64{}
			for _, h := range filterRelevantHits(tc.hits) {
				got = append(got, h.ChunkID)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAskRefusesWhenNothingPasses(t *testing.T) {
	tests := []struct {
		name string
		hits []SearchHit
	}{
		{"no hits", nil},
		{"all below relevance cutoff", []SearchHit{ragHit(1, "Method", 0.2), ragHit(2, "Intro", 0.44)}},
		{"only weak auxiliary sections", []SearchHit{ragHit(1, "Appendix B", 0.6), ragHit(2, "Supplementary", 0.5)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			emb := simEmbedder(constSim(1))
			r := &RAGService{searchEngine: &fakeSearcher{hits: tc.hits}, embedClient: emb}
			resp, err := r.Ask(context.Background(), "q", nil, 3)
			if err != nil || resp.Answer != "No indexed papers are relevant to this question." ||
				resp.Citations == nil || len(resp.Citations) != 0 || len(resp.TraceID) != 32 {
				t.Errorf("got %+v err %v, want refusal with empty non-nil citations and a trace id", resp, err)
			}
			if len(emb.texts) != 0 {
				t.Errorf("refusal must not call the embedder, got %v", emb.texts)
			}
		})
	}
}

func TestAskAnswersWithValidatedCitations(t *testing.T) {
	s := &fakeSearcher{hits: []SearchHit{ragHit(1, "Method", 0.9), ragHit(2, "Appendix", 0.3), ragHit(3, "Results", 0.8)}}
	r := &RAGService{searchEngine: s, embedClient: simEmbedder(constSim(0.8))}
	resp, err := r.Ask(context.Background(), "q", []string{"cs.LG"}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.gotLimit != 5 || !s.gotRerank {
		t.Errorf("HybridSearch(limit=%d, rerank=%v), want default limit 5 and rerank", s.gotLimit, s.gotRerank)
	}
	if got := markers(resp.Citations); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("markers = %v, want [1 2] (answer %q)", got, resp.Answer)
	}
	// Marker 2 maps to the second *surviving* hit (chunk 3), not the dropped appendix.
	c := resp.Citations[1]
	if c.ChunkID != 3 || c.Section != "Results" || c.PDFURL != "https://arxiv.org/pdf/"+c.ArxivID+".pdf" {
		t.Errorf("citation 2 = %+v", c)
	}
	if !c.Supported || c.Score != 0.8 {
		t.Errorf("citation 2 score=%v supported=%v, want 0.8 true", c.Score, c.Supported)
	}

	// One surviving hit: the template emits only [1].
	s.hits = []SearchHit{ragHit(1, "Method", 0.9)}
	if resp, err = r.Ask(context.Background(), "q", nil, 5); err != nil || strings.Contains(resp.Answer, "[2]") ||
		!reflect.DeepEqual(markers(resp.Citations), []int{1}) {
		t.Errorf("single hit: answer %q citations %v err %v", resp.Answer, markers(resp.Citations), err)
	}

	// Retrieval errors are wrapped and returned, never turned into an answer.
	s.err = errors.New("db down")
	if resp, err = r.Ask(context.Background(), "q", nil, 5); err == nil || resp != nil ||
		!strings.Contains(err.Error(), "retrieval failed: db down") {
		t.Errorf("got resp=%v err=%v, want wrapped retrieval error", resp, err)
	}
}

func TestValidateCitationsMarkers(t *testing.T) {
	hits := []SearchHit{ragHit(1, "A", 0.9), ragHit(2, "B", 0.9)}
	tests := []struct {
		name, answer string
		want         []int
	}{
		{"in range", "Claim [1]. Other claim [2].", []int{1, 2}},
		{"marker above K discarded", "Claim [3]. Other [2].", []int{2}},
		{"marker zero discarded", "Claim [0].", []int{}},
		{"overflowing marker discarded", "Claim [99999999999999999999].", []int{}},
		{"multiple markers per sentence", "Both agree [1][2].", []int{1, 2}},
		{"multiple markers mixed valid and fabricated", "X [2] and Y [7] and Z [1].", []int{2, 1}},
		{"repeated marker across sentences", "First [1]. Again [1].", []int{1, 1}},
		{"non-numeric brackets ignored", "See [a] and [-1] and [ 1 ].", []int{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			emb := simEmbedder(constSim(0.9))
			got := (&RAGService{embedClient: emb}).validateCitations(context.Background(), tc.answer, hits)
			if m := markers(got); !reflect.DeepEqual(m, tc.want) {
				t.Errorf("markers = %v, want %v", m, tc.want)
			}
			for _, c := range got {
				if c.ChunkID != hits[c.Marker-1].ChunkID {
					t.Errorf("marker %d mapped to chunk %d", c.Marker, c.ChunkID)
				}
			}
			for _, txt := range emb.texts {
				if markerRegex.MatchString(txt) {
					t.Errorf("embedded text %q still contains a marker", txt)
				}
			}
		})
	}
}

func TestValidateCitationsSupportThreshold(t *testing.T) {
	if SupportThreshold != 0.50 {
		t.Fatalf("SupportThreshold = %v; this table assumes 0.50", SupportThreshold)
	}
	tests := []struct {
		name    string
		sim     float64
		wantSup bool
	}{
		{"0.49 unsupported", 0.49, false},
		{"0.4999 unsupported", 0.4999, false},
		{"0.50 supported", 0.50, true},
		{"negative unsupported", -0.3, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &RAGService{embedClient: simEmbedder(constSim(tc.sim))}
			got := r.validateCitations(context.Background(), "Claim [1].", []SearchHit{ragHit(1, "A", 0.9)})
			if len(got) != 1 || got[0].Score != tc.sim || got[0].Supported != tc.wantSup {
				t.Errorf("got %+v, want score %v supported %v", got, tc.sim, tc.wantSup)
			}
		})
	}

	// Support is judged per sentence, even for the same marker.
	r := &RAGService{embedClient: simEmbedder(func(s string) float64 {
		if strings.HasPrefix(s, "Strong") {
			return 0.9
		}
		return 0.2
	})}
	got := r.validateCitations(context.Background(), "Strong claim [1]. Weak claim [1].", []SearchHit{ragHit(1, "A", 0.9)})
	if len(got) != 2 || !got[0].Supported || got[1].Supported {
		t.Errorf("per-sentence: got %+v, want first supported, second unsupported", got)
	}
}

// AI-2 (QA-4): when support cannot be verified, the citation fails closed (Score 0, Supported false)
// and the request logs exactly one line with counts only (no sentence or passage text).
func TestValidateCitationsFailClosedWhenUnverifiable(t *testing.T) {
	isChunk := func(t string) bool { return strings.HasPrefix(t, "chunk") }
	// vecs builds a fake that returns chunkVec for chunks and sentVec for sentences (nil = no vector).
	vecs := func(chunkVec, sentVec []float32) func(string) []float32 {
		return func(t string) []float32 {
			if isChunk(t) {
				return chunkVec
			}
			return sentVec
		}
	}
	ok := []float32{1, 0}
	tests := []struct {
		name string
		emb  *fakeEmbedder
	}{
		{"embedder down for every call", &fakeEmbedder{err: errors.New("down")}},
		{"sentence embed error", &fakeEmbedder{vec: vecs(ok, ok), failOn: func(t string) bool { return !isChunk(t) }}},
		{"chunk embed error", &fakeEmbedder{vec: vecs(ok, ok), failOn: isChunk}},
		{"sentence returns no vector", &fakeEmbedder{vec: vecs(ok, nil)}},
		{"sentence returns empty vector", &fakeEmbedder{vec: vecs(ok, []float32{})}},
		{"chunk returns no vector", &fakeEmbedder{vec: vecs(nil, ok)}},
		{"chunk returns empty vector", &fakeEmbedder{vec: vecs([]float32{}, ok)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			prevOut, prevFlags := log.Writer(), log.Flags()
			log.SetOutput(&buf)
			log.SetFlags(0)
			t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })

			ctx := withTraceID(context.Background(), "t-ai2")
			got := (&RAGService{embedClient: tc.emb}).validateCitations(ctx, "Claim one [1]. Claim two [1].", []SearchHit{ragHit(1, "A", 0.9)})
			if len(got) != 2 {
				t.Fatalf("got %d citations, want 2 (markers are kept, only support is withheld): %+v", len(got), got)
			}
			for _, c := range got {
				if c.Score != 0 || c.Supported {
					t.Errorf("marker %d score=%v supported=%v, want 0 false", c.Marker, c.Score, c.Supported)
				}
			}
			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], "citation support unverified trace_id=t-ai2 ") ||
				!strings.Contains(lines[0], "unverified=2 citations=2") {
				t.Errorf("want exactly one unverified log line with counts, got %q", buf.String())
			}
			for _, secret := range []string{"Claim", "chunk A"} {
				if strings.Contains(buf.String(), secret) {
					t.Errorf("log leaks text %q: %q", secret, buf.String())
				}
			}
		})
	}

	// Verified citations log nothing.
	var buf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prevOut)
	got := (&RAGService{embedClient: simEmbedder(constSim(0.9))}).validateCitations(context.Background(), "Claim [1].", []SearchHit{ragHit(1, "A", 0.9)})
	if len(got) != 1 || !got[0].Supported || buf.Len() != 0 {
		t.Errorf("verified path: got %+v log %q, want supported and no log", got, buf.String())
	}
}

func TestSplitIntoSentences(t *testing.T) {
	tests := []struct {
		name, in string
		want     []string
	}{
		{"empty", "", nil},
		{"no terminator", "no terminator here", []string{"no terminator here"}},
		{"two sentences", "First one. Second one.", []string{"First one.", "Second one."}},
		{"question and exclamation", "Why? Because! Done.", []string{"Why?", "Because!", "Done."}},
		{"decimal not split", "Accuracy rose to 0.95 [1]. Next [2].", []string{"Accuracy rose to 0.95 [1].", "Next [2]."}},
		// Bug fixed in AI-1: abbreviations used to end sentences and separate markers from their claims.
		{"et al. not split", "Smith et al. [1] show gains. Next.", []string{"Smith et al. [1] show gains.", "Next."}},
		{"e.g. not split", "Use priors, e.g. dropout [1].", []string{"Use priors, e.g. dropout [1]."}},
		{"i.e. in parens not split", "A loss (i.e. CE) works [1].", []string{"A loss (i.e. CE) works [1]."}},
		{"vs. and Fig. not split", "CNN vs. RNN in Fig. 2 [1].", []string{"CNN vs. RNN in Fig. 2 [1]."}},
		{"abbreviation suffix inside a word still splits", "We tune the config. Then run.", []string{"We tune the config.", "Then run."}},
		{"abbreviation at end of text", "Many methods, e.g.", []string{"Many methods, e.g."}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := splitIntoSentences(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float32
	}{
		{"both zero", []float32{0, 0, 0}, []float32{0, 0, 0}, 0},
		{"one zero", []float32{0, 0}, []float32{1, 2}, 0},
		{"empty", []float32{}, []float32{}, 0},
		{"length mismatch", []float32{1, 0}, []float32{1, 0, 0}, 0},
		{"identical", []float32{3, 4}, []float32{3, 4}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite", []float32{1, 2}, []float32{-1, -2}, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cosineSimilarity(tc.a, tc.b)
			if math.IsNaN(float64(got)) || math.Abs(float64(got-tc.want)) > 1e-6 {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func sortedKeys(raw []byte) (out []string) {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AC3: the /v1/ask JSON shape is unchanged, and NewRAGService still takes the concrete types.
func TestAskResponseShapeAndConstructor(t *testing.T) {
	top, _ := json.Marshal(AskResponse{})
	cite, _ := json.Marshal(Citation{})
	if got, want := sortedKeys(top), []string{"answer", "citations", "trace_id"}; !reflect.DeepEqual(got, want) {
		t.Errorf("response keys %v, want %v", got, want)
	}
	want := []string{"arxiv_id", "chunk_id", "content", "marker", "pdf_url", "score", "section", "supported", "title"}
	if got := sortedKeys(cite); !reflect.DeepEqual(got, want) {
		t.Errorf("citation keys %v, want %v", got, want)
	}
	se, ec := &SearchEngine{}, &EmbedClient{}
	if r := NewRAGService(se, ec); r.searchEngine != searcher(se) || r.embedClient != embedder(ec) {
		t.Errorf("dependencies not wired: %+v", r)
	}
}

// countingEmbedder wraps an embedder, records the texts of every EmbedTexts call, and can fail or
// reshape a given call (1-based call number) to exercise AI-3's batch fail-closed paths.
type countingEmbedder struct {
	inner    embedder
	calls    [][]string
	failCall map[int]error
	reshape  map[int]func([][]float32) [][]float32
}

func (c *countingEmbedder) EmbedTexts(ctx context.Context, texts []string, isQuery bool) ([][]float32, error) {
	c.calls = append(c.calls, append([]string(nil), texts...))
	n := len(c.calls)
	if err := c.failCall[n]; err != nil {
		return nil, err
	}
	vecs, err := c.inner.EmbedTexts(ctx, texts, isQuery)
	if f := c.reshape[n]; f != nil && err == nil {
		vecs = f(vecs)
	}
	return vecs, err
}

// batchHits are three relevant hits whose chunk vectors differ, so a batch result applied to the
// wrong input index changes the scores.
func batchHits() []SearchHit {
	return []SearchHit{ragHit(1, "Method", 0.9), ragHit(2, "Results", 0.8), ragHit(3, "Discussion", 0.7)}
}

func batchVectors() *fakeEmbedder {
	return &fakeEmbedder{vec: func(t string) []float32 {
		switch {
		case t == "chunk Method":
			return []float32{1, 0}
		case t == "chunk Results":
			return []float32{0, 1}
		case t == "chunk Discussion":
			return []float32{0.6, 0.8}
		case strings.HasPrefix(t, "Furthermore"):
			return []float32{0.6, 0.8} // cos 0.8 with chunk Results
		default:
			return []float32{0.9, 0.1}
		}
	}}
}

// perItemCitations is the pre-AI-3 algorithm: one EmbedTexts call per chunk and per cited sentence.
// It is the reference the batched implementation must match.
func perItemCitations(t *testing.T, emb embedder, answer string, hits []SearchHit) []Citation {
	t.Helper()
	embedOne := func(text string) []float32 {
		v, err := emb.EmbedTexts(context.Background(), []string{text}, false)
		if err != nil || len(v) == 0 {
			return nil
		}
		return v[0]
	}
	var out []Citation
	for _, sent := range splitIntoSentences(answer) {
		for _, m := range markerRegex.FindAllStringSubmatch(sent, -1) {
			n, _ := strconv.Atoi(m[1])
			if n < 1 || n > len(hits) {
				continue
			}
			sv := embedOne(strings.TrimSpace(markerRegex.ReplaceAllString(sent, "")))
			cv := embedOne(hits[n-1].Content)
			score := 0.0
			if len(sv) > 0 && len(cv) > 0 {
				score = math.Round(float64(cosineSimilarity(sv, cv))*10000) / 10000
			}
			out = append(out, Citation{Marker: n, ChunkID: hits[n-1].ChunkID, Score: score, Supported: score >= SupportThreshold})
		}
	}
	return out
}

type labelScore struct {
	Marker    int
	ChunkID   int64
	Score     float64
	Supported bool
}

func labelsAndScores(cs []Citation) []labelScore {
	out := []labelScore{}
	for _, c := range cs {
		out = append(out, labelScore{c.Marker, c.ChunkID, c.Score, c.Supported})
	}
	return out
}

// AI-3 AC1/AC5: an answer over 3 hits with 2 cited sentences costs at most 2 EmbedTexts calls
// (all chunks, then all sentences), and the labels/scores equal the one-call-per-item results.
func TestAskBatchesEmbeddingCalls(t *testing.T) {
	emb := &countingEmbedder{inner: batchVectors()}
	r := &RAGService{searchEngine: &fakeSearcher{hits: batchHits()}, embedClient: emb}
	resp, err := r.Ask(context.Background(), "q", nil, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := markers(resp.Citations); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("markers = %v, want [1 2] (answer %q)", got, resp.Answer)
	}
	if len(emb.calls) > 2 {
		t.Fatalf("EmbedTexts called %d times, want <= 2 (one per batch): %q", len(emb.calls), emb.calls)
	}
	if len(emb.calls) != 2 || !reflect.DeepEqual(emb.calls[0], []string{"chunk Method", "chunk Results", "chunk Discussion"}) ||
		len(emb.calls[1]) != 2 {
		t.Errorf("calls = %q, want [all 3 chunks in hit order] then [2 sentences]", emb.calls)
	}

	want := perItemCitations(t, batchVectors(), resp.Answer, batchHits())
	if got := labelsAndScores(resp.Citations); !reflect.DeepEqual(got, labelsAndScores(want)) {
		t.Errorf("batched %+v, per-item %+v", got, labelsAndScores(want))
	}
	if c := resp.Citations[1]; c.Score != 0.8 || !c.Supported {
		t.Errorf("citation 2 = %+v, want score 0.8 supported (chunk vectors kept in input order)", c)
	}
}

// AI-3: batched validateCitations matches the per-item reference on the existing AI-1/AI-2 inputs,
// deduplicates sentences, and never exceeds 2 calls.
func TestValidateCitationsBatchMatchesPerItem(t *testing.T) {
	hits := []SearchHit{ragHit(1, "A", 0.9), ragHit(2, "B", 0.9)}
	perSentence := simEmbedder(func(s string) float64 {
		if strings.HasPrefix(s, "Strong") {
			return 0.9
		}
		return 0.2
	})
	tests := []struct {
		name, answer string
		inner        *fakeEmbedder
		wantSentence []string // texts of the sentence batch; nil = no sentence call expected
		wantCalls    int
	}{
		{"per-sentence support", "Strong claim [1]. Weak claim [1].", perSentence, []string{"Strong claim .", "Weak claim ."}, 2},
		{"multiple markers one sentence embedded once", "X [2] and Y [7] and Z [1].", simEmbedder(constSim(0.9)), []string{"X  and Y  and Z ."}, 2},
		{"repeated identical sentence embedded once", "Same [1]. Same [2]. Same [1].", perSentence, []string{"Same ."}, 2},
		{"threshold edge", "Claim [1]. Other [2].", simEmbedder(constSim(0.4999)), []string{"Claim .", "Other ."}, 2},
		{"no in-range marker makes no call", "Claim [0]. Other [3].", simEmbedder(constSim(0.9)), nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			emb := &countingEmbedder{inner: tc.inner}
			got := (&RAGService{embedClient: emb}).validateCitations(context.Background(), tc.answer, hits)
			want := perItemCitations(t, tc.inner, tc.answer, hits)
			if !reflect.DeepEqual(labelsAndScores(got), labelsAndScores(want)) {
				t.Errorf("batched %+v, per-item %+v", labelsAndScores(got), labelsAndScores(want))
			}
			if len(emb.calls) != tc.wantCalls {
				t.Fatalf("calls = %d (%q), want %d", len(emb.calls), emb.calls, tc.wantCalls)
			}
			if tc.wantCalls == 2 && !reflect.DeepEqual(emb.calls[1], tc.wantSentence) {
				t.Errorf("sentence batch = %q, want %q", emb.calls[1], tc.wantSentence)
			}
		})
	}
}

// AI-3 AC4: a failed or mis-sized batch fails closed for every citation that depends on it,
// never panics, still makes at most 2 calls, and keeps the AI-2 log counters' meaning.
func TestAskBatchFailClosed(t *testing.T) {
	drop := func(v [][]float32) [][]float32 { return v[:len(v)-1] }
	extra := func(v [][]float32) [][]float32 { return append(v, []float32{1, 0}) }
	tests := []struct {
		name     string
		failCall map[int]error
		reshape  map[int]func([][]float32) [][]float32
		wantLog  string
	}{
		{"(a) chunk batch errors", map[int]error{1: errors.New("down")}, nil,
			"unverified=2 citations=2 chunks_without_vector=3/3 sentence_embed_errors=0"},
		{"(b) sentence batch errors", map[int]error{2: errors.New("down")}, nil,
			"unverified=2 citations=2 chunks_without_vector=0/3 sentence_embed_errors=2"},
		{"(c) chunk batch returns fewer vectors", nil, map[int]func([][]float32) [][]float32{1: drop},
			"unverified=2 citations=2 chunks_without_vector=3/3 sentence_embed_errors=0"},
		{"(c) sentence batch returns fewer vectors", nil, map[int]func([][]float32) [][]float32{2: drop},
			"unverified=2 citations=2 chunks_without_vector=0/3 sentence_embed_errors=0"},
		{"chunk batch returns more vectors", nil, map[int]func([][]float32) [][]float32{1: extra},
			"unverified=2 citations=2 chunks_without_vector=3/3 sentence_embed_errors=0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			prevOut, prevFlags := log.Writer(), log.Flags()
			log.SetOutput(&buf)
			log.SetFlags(0)
			t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })

			emb := &countingEmbedder{inner: batchVectors(), failCall: tc.failCall, reshape: tc.reshape}
			r := &RAGService{searchEngine: &fakeSearcher{hits: batchHits()}, embedClient: emb}
			resp, err := r.Ask(context.Background(), "q", nil, 3)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(resp.Citations) != 2 {
				t.Fatalf("got %d citations, want 2 (markers kept, support withheld)", len(resp.Citations))
			}
			for _, c := range resp.Citations {
				if c.Score != 0 || c.Supported {
					t.Errorf("marker %d score=%v supported=%v, want 0 false", c.Marker, c.Score, c.Supported)
				}
			}
			if len(emb.calls) != 2 {
				t.Errorf("EmbedTexts called %d times, want 2", len(emb.calls))
			}
			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], tc.wantLog) {
				t.Errorf("log = %q, want one line containing %q", buf.String(), tc.wantLog)
			}
		})
	}
}
