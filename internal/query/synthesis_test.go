package query

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"arxivlens/internal/llm"
	"arxivlens/internal/llm/llmtest"
)

// byPassage scores a pair by the section named in its passage (hitContent embeds the section name).
func byPassage(scores map[string]float64) *fakeVerifier {
	return &fakeVerifier{score: func(_, passage string) float64 {
		for sec, s := range scores {
			if passage == hitContent(sec) {
				return s
			}
		}
		return 0
	}}
}

func llmRAG(hits []SearchHit, v verifier, steps ...llmtest.Step) (*RAGService, *llmtest.Fake) {
	f := llmtest.New(steps...)
	return newRAG(hits, v).WithLLM(f), f
}

const twoClaims = `{"claims": [{"text": "The method improves benchmark accuracy.", "citations": [1]},
 {"text": "Results confirm the gain.", "citations": [2, 3]}], "insufficient_evidence": false}`

func TestSynthesisAnswersWithVerifiedClaims(t *testing.T) {
	buf := captureLog(t)
	v := byPassage(map[string]float64{"Method": 0.9, "Results": 0.2, "Discussion": 0.8})
	r, f := llmRAG(batchHits(), v, llmtest.Reply(twoClaims))
	resp, err := r.Ask(context.Background(), "secret question?", nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := "The method improves benchmark accuracy [1]. Results confirm the gain [2][3]."; resp.Answer != want || resp.RefusalReason != "" {
		t.Fatalf("answer = %q (reason %q), want %q", resp.Answer, resp.RefusalReason, want)
	}
	got := []citeLabel{}
	for _, c := range resp.Citations {
		got = append(got, citeLabel{c.Marker, c.ChunkID, c.Score, c.Supported})
	}
	want := []citeLabel{{1, 1, 0.9, true}, {2, 2, 0.2, false}, {3, 3, 0.8, true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("citations = %+v, want %+v", got, want)
	}
	// One JSON-mode, temperature-0 request with the versioned system prompt and the question in the user turn.
	reqs := f.Requests()
	if len(reqs) != 1 || reqs[0].System != synthesisSystemPrompt || !reqs[0].JSON || reqs[0].Temperature != 0 ||
		len(reqs[0].Messages) != 1 || !strings.Contains(reqs[0].Messages[0].Content, "<question>\nsecret question?\n</question>") {
		t.Errorf("requests = %v", reqs)
	}
	// Each claim is checked against the passage it cites, as the model saw it; 3 pairs in one verifier call.
	if len(v.calls) != 1 || len(v.calls[0]) != 3 || v.calls[0][2].Claim != "Results confirm the gain." || v.calls[0][2].Passage != hitContent("Discussion") {
		t.Errorf("verify calls = %+v", v.calls)
	}
	// Logs carry counts and kinds only.
	if !strings.Contains(buf.String(), "synthesis trace_id=") || !strings.Contains(buf.String(), "claims=2") ||
		strings.Contains(buf.String(), "secret question") || strings.Contains(buf.String(), "benchmark accuracy") {
		t.Errorf("log = %q", buf.String())
	}
}

type citeLabel struct {
	Marker    int
	ChunkID   int64
	Score     float64
	Supported bool
}

func TestSynthesisDropsUnsupportedClaims(t *testing.T) {
	buf := captureLog(t)
	reply := `{"claims": [{"text": "Supported claim.", "citations": [1]}, {"text": "Unsupported claim!", "citations": [2]}]}`
	r, _ := llmRAG(batchHits(), byPassage(map[string]float64{"Method": 0.95, "Results": 0.1}), llmtest.Reply(reply))
	resp, _ := r.Ask(context.Background(), "q", nil, 3)
	if resp.Answer != "Supported claim [1]." || !reflect.DeepEqual(markers(resp.Citations), []int{1}) {
		t.Errorf("answer=%q markers=%v, want only the supported claim", resp.Answer, markers(resp.Citations))
	}
	if !strings.Contains(buf.String(), "dropped unsupported claims") || !strings.Contains(buf.String(), "claims=2 kept=1") ||
		strings.Contains(buf.String(), "Unsupported claim") {
		t.Errorf("log = %q", buf.String())
	}
}

func TestSynthesisRefusals(t *testing.T) {
	t.Run("no claim verified: all_citations_unsupported with every passage as evidence", func(t *testing.T) {
		buf := captureLog(t)
		r, _ := llmRAG(batchHits(), byPassage(map[string]float64{"Method": 0.3, "Results": 0.1, "Discussion": 0.2}), llmtest.Reply(twoClaims))
		resp, _ := r.Ask(context.Background(), "secret question", nil, 3)
		if resp.RefusalReason != reasonAllUnsupported || resp.Answer != refusalUnsupported {
			t.Fatalf("resp = %+v", resp)
		}
		checkEvidence(t, resp.Citations, batchHits(), []float64{0.3, 0.1, 0.2})
		checkRefusalLog(t, buf, resp.TraceID, "reason=all_citations_unsupported citations=3 checked=3 unverified=0 evidence=3")
	})
	t.Run("verifier down: verification_unavailable", func(t *testing.T) {
		captureLog(t)
		v := &fakeVerifier{score: func(string, string) float64 { return 1 }, err: errors.New("down")}
		r, _ := llmRAG(batchHits(), v, llmtest.Reply(twoClaims))
		resp, _ := r.Ask(context.Background(), "q", nil, 3)
		if resp.RefusalReason != reasonVerificationUnavailable {
			t.Fatalf("resp = %+v", resp)
		}
		checkEvidence(t, resp.Citations, batchHits(), []float64{0, 0, 0})
	})
	t.Run("model says the passages do not answer: insufficient_evidence, no verification", func(t *testing.T) {
		buf := captureLog(t)
		v := simVerifier(constSim(1))
		r, _ := llmRAG(batchHits(), v, llmtest.Reply(`{"claims": [], "insufficient_evidence": true}`))
		resp, _ := r.Ask(context.Background(), "secret question", nil, 3)
		if resp.RefusalReason != reasonInsufficientEvidence || resp.Answer != refusalInsufficient || len(v.calls) != 0 {
			t.Fatalf("resp = %+v, verify calls %d", resp, len(v.calls))
		}
		checkEvidence(t, resp.Citations, batchHits(), []float64{0, 0, 0})
		raw, _ := json.Marshal(resp)
		if !strings.Contains(string(raw), `"refusal_reason":"insufficient_evidence"`) {
			t.Errorf("JSON %s", raw)
		}
		checkRefusalLog(t, buf, resp.TraceID, "reason=insufficient_evidence citations=0 checked=0 unverified=0 evidence=3")
	})
}

// Any LLM failure or unusable reply falls back to the verified extractive answer, with one counts-only log line.
func TestSynthesisFallsBackToExtractive(t *testing.T) {
	extractive := (&RAGService{extractor: prefersImproves()}).buildExtractiveAnswer(context.Background(), "q", batchHits())
	for _, tc := range []struct {
		name   string
		step   llmtest.Step
		reason string
	}{
		{"timeout", llmtest.Fail(&llm.Error{Kind: llm.ErrTimeout}), "llm_timeout"},
		{"rate limited", llmtest.Fail(&llm.Error{Kind: llm.ErrRateLimited}), "llm_rate_limited"},
		{"provider error", llmtest.Fail(&llm.Error{Kind: llm.ErrProvider}), "llm_error"},
		{"prose instead of JSON", llmtest.Reply("The answer is A [1]."), "malformed_reply"},
		{"no claims and not insufficient", llmtest.Reply(`{"claims": [], "insufficient_evidence": false}`), "no_claims"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLog(t)
			r, _ := llmRAG(batchHits(), simVerifier(constSim(0)), tc.step)
			resp, err := r.Ask(context.Background(), "secret question", nil, 3)
			if err != nil || resp.Answer != extractive || resp.RefusalReason != "" {
				t.Fatalf("resp=%+v err=%v, want the extractive answer", resp, err)
			}
			if !strings.Contains(buf.String(), "synthesis fallback") || !strings.Contains(buf.String(), "reason="+tc.reason+" provider=fake") ||
				strings.Contains(buf.String(), "secret question") {
				t.Errorf("log = %q, want one fallback line with reason=%s", buf.String(), tc.reason)
			}
		})
	}
}

// Claims that cite only passages that do not exist are dropped by the parser; the answer is refused (AI-6b design:
// the model asserted things it could not cite), never replaced by the extractive answer.
func TestSynthesisRefusesWhenEveryCitationIsFabricated(t *testing.T) {
	buf := captureLog(t)
	v := simVerifier(constSim(1))
	r, _ := llmRAG(batchHits(), v, llmtest.Reply(`{"claims": [{"text": "X.", "citations": [9]}]}`))
	resp, _ := r.Ask(context.Background(), "q", nil, 3)
	if resp.RefusalReason != reasonAllUnsupported || len(v.calls) != 0 || strings.Contains(buf.String(), "synthesis fallback") {
		t.Fatalf("resp=%+v verify calls=%d log=%q", resp, len(v.calls), buf.String())
	}
	if !strings.Contains(buf.String(), "dropped_claims=1 dropped_markers=1") {
		t.Errorf("log = %q, want the dropped counts", buf.String())
	}
	checkEvidence(t, resp.Citations, batchHits(), []float64{0, 0, 0})
}

// Markers number passages in prompt order, which groups each paper's passages after its best one.
func TestSynthesisMarkersFollowPromptOrder(t *testing.T) {
	captureLog(t)
	a1, b1, a2 := ragHit(1, "Alpha", 0.9), ragHit(2, "Beta", 0.8), ragHit(3, "Gamma", 0.7)
	a2.ArxivID = a1.ArxivID // a1 and a2 are the same paper: prompt order a1, a2, b1
	reply := `{"claims": [{"text": "The second passage says so.", "citations": [2]}]}`
	v := byPassage(map[string]float64{"Gamma": 0.9})
	r, _ := llmRAG([]SearchHit{a1, b1, a2}, v, llmtest.Reply(reply))
	resp, _ := r.Ask(context.Background(), "q", nil, 3)
	if len(resp.Citations) != 1 || resp.Citations[0].ChunkID != 3 || resp.Citations[0].Marker != 2 || !resp.Citations[0].Supported {
		t.Errorf("citations = %+v, want marker 2 -> chunk 3 (a2), supported", resp.Citations)
	}
}

func TestWithMarkers(t *testing.T) {
	for in, want := range map[string]string{
		"X holds.":  "X holds [1][2].",
		"Is it?":    "Is it [1][2]?",
		"No stop":   "No stop [1][2].",
		"  Pad.  ":  "Pad [1][2].",
		"Ends in .": "Ends in [1][2].",
	} {
		if got := withMarkers(in, []int{1, 2}); got != want {
			t.Errorf("withMarkers(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWithLLMNilKeepsExtractive(t *testing.T) {
	r := newRAG(batchHits(), simVerifier(constSim(0))).WithLLM(nil)
	resp, _ := r.Ask(context.Background(), "q", nil, 3)
	if resp.RefusalReason != "" || !strings.Contains(resp.Answer, "improves accuracy") {
		t.Errorf("resp = %+v", resp)
	}
}
