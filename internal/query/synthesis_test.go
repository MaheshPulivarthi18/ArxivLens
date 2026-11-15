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
	// No claim verified, or the verifier is down: the quoted answer replaces the synthesis and is judged on its own
	// terms (TestSynthesisClaimsUnsupportedFallBackToQuotes). Only the model's own "insufficient" is a refusal here.
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

// When no LLM claim survives verification (unsupported, unverifiable, or every citation fabricated), the reader
// gets the quoted answer, which is checked on its own terms and refuses by itself when nothing is supported.
func TestSynthesisClaimsUnsupportedFallBackToQuotes(t *testing.T) {
	extractive := (&RAGService{extractor: prefersImproves()}).buildExtractiveAnswer(context.Background(), "q", batchHits())
	for _, tc := range []struct {
		name  string
		v     *fakeVerifier
		reply string
		logs  string
	}{
		{"claims unsupported", byPassage(map[string]float64{"Method": 0.3, "Results": 0.1, "Discussion": 0.2}), twoClaims,
			"claims=2 citations=3 checked=3 unverified=0"},
		{"citations fabricated", simVerifier(constSim(1)), `{"claims": [{"text": "X.", "citations": [9]}]}`,
			"claims=0 citations=0 checked=0 unverified=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLog(t)
			r, _ := llmRAG(batchHits(), tc.v, llmtest.Reply(tc.reply))
			resp, _ := r.Ask(context.Background(), "secret question", nil, 3)
			if resp.Answer != extractive || resp.RefusalReason != "" || resp.AnswerKind != AnswerQuoted {
				t.Fatalf("resp = %+v, want the quoted answer", resp)
			}
			if !strings.Contains(buf.String(), "synthesis claims unsupported") || !strings.Contains(buf.String(), tc.logs) ||
				!strings.Contains(buf.String(), "reason=claims_unsupported") || strings.Contains(buf.String(), "secret question") {
				t.Errorf("log = %q", buf.String())
			}
		})
	}
	t.Run("verifier down: verbatim quotes still verify by string match", func(t *testing.T) {
		captureLog(t)
		v := &fakeVerifier{score: func(string, string) float64 { return 1 }, err: errors.New("down")}
		r, _ := llmRAG(batchHits(), v, llmtest.Reply(twoClaims))
		resp, _ := r.Ask(context.Background(), "q", nil, 3)
		if resp.Answer != extractive || resp.AnswerKind != AnswerQuoted {
			t.Fatalf("resp = %+v", resp)
		}
		for _, c := range resp.Citations {
			if !c.Supported || c.Score != 1 {
				t.Errorf("citation %+v, want a quote match", c)
			}
		}
	})
}

// Quick answers skip the LLM and say a synthesized answer can be requested; without an LLM they never say so.
func TestQuickAnswerSkipsSynthesis(t *testing.T) {
	captureLog(t)
	r, f := llmRAG(batchHits(), simVerifier(constSim(1)), llmtest.Reply(twoClaims))
	resp, _ := r.AskWith(context.Background(), "q", nil, 3, AskOptions{Quick: true})
	if len(f.Requests()) != 0 || resp.AnswerKind != AnswerQuoted || !resp.SynthesisAvailable {
		t.Fatalf("resp = %+v, llm requests %d", resp, len(f.Requests()))
	}
	full, _ := r.AskWith(context.Background(), "q", nil, 3, AskOptions{})
	if full.AnswerKind != AnswerSynthesized || full.SynthesisAvailable {
		t.Errorf("full = %+v", full)
	}
	plain, _ := newRAG(batchHits(), simVerifier(constSim(1))).AskWith(context.Background(), "q", nil, 3, AskOptions{Quick: true})
	if plain.SynthesisAvailable || plain.AnswerKind != AnswerQuoted {
		t.Errorf("no-LLM quick = %+v", plain)
	}
	raw, _ := json.Marshal(resp)
	if !strings.Contains(string(raw), `"answer_kind":"quoted","synthesis_available":true`) {
		t.Errorf("JSON %s", raw)
	}
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
