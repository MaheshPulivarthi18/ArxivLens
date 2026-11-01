# ADR-0006: Definition of Recall@5 for the retrieval quality target

- **Status:** Proposed
- **Date:** 2026-11-01
- **Deciders:** Mahesh Pulivarthi (only the human owner sets Accepted/Rejected)
- **Blocks:** nothing directly. It affects how the Product PRD §8 target "Recall@5 ≥ 0.80" (`retrieval_recall_at_5_min` in `evals/thresholds.json`, EVAL-1) is read, and any later change to `scripts/evaluate_retrieval.py`.

## Context
Product PRD §8 sets "Retrieval Recall@5 on eval set ≥ 0.80". EVAL-1 (PR #2) gates on it. The eval cases in
`evals/retrieval.jsonl` label relevance at the **paper** level (`relevant_arxiv_ids`). But
`scripts/evaluate_retrieval.py` scores at the **chunk** level:

- "relevant" = every chunk of every relevant paper (often 30–60 chunks per paper)
- `recall@5 = |top-5 ∩ relevant chunks| / min(|relevant chunks|, 5)`

Because a relevant paper almost always has ≥ 5 chunks, the denominator is 5, so the metric is really **precision@5**:
the share of the top 5 chunks that come from any relevant paper. It is strict. A query whose single best passage ranks
first, followed by 4 good passages from other papers, scores 0.2. MRR and NDCG@5 use the same chunk-level relevance.
The PRD target was written without saying which definition it meant. The ai-engineer flagged this in EVAL-1 Notes.

## Options
1. **Paper-level Recall@5.** Dedupe the ranked chunks to arXiv IDs in rank order, then compute `|top-5 papers ∩ relevant papers| / |relevant papers|`. MRR and NDCG@5 use the same paper-level ranking. This matches the labels and the user question ("did the right paper show up?"), and it is standard IR recall. Weakness: it says nothing about whether the *right passage* surfaced.
2. **Keep chunk-level as-is (precision@5-like), and rename it.** Call it "Relevant-chunk precision@5" in the PRD and in the output. No code change to the math, and it is a strict signal for answer grounding. The 0.80 target is then very demanding and probably wrong for multi-paper topics. A threshold change would need this ADR anyway.
3. **Report both, gate on paper-level.** Gate on paper-level Recall@5 ≥ 0.80. Also report chunk precision@5, ungated, as a diagnostic. This keeps the strict signal visible without blocking on it. It costs a small change in `scripts/evaluate_retrieval.py` and one more key in the results JSON.
4. **Passage-level labels.** Label the specific relevant chunks per query. This is the most precise option, but labels break whenever chunking changes (chunk IDs are not stable across re-chunking), so it is expensive to maintain.

## Recommendation (from pm-lead)
Option 3. Paper-level recall matches how the eval set is labelled and what the PRD target means to a user. Keeping
chunk precision@5 as an ungated diagnostic means we still see grounding quality. The threshold value (0.80) does not change, only
the definition it applies to. That needs this ADR because it changes what a gated metric means. If accepted, the ai-engineer
gets a follow-up task (est. < 80 lines in `scripts/evaluate_retrieval.py` + `tests/test_evals_data.py`-style unit
tests for the metric functions), and `evals/BASELINE.md` is re-recorded.

## Decision
_(Filled by the human.)_

## Consequences
- Option 1/3: the gate gets easier to pass than it is today, and the baseline must be re-run so before/after comparisons stay valid.
- Option 2: PRD §8 wording and possibly the 0.80 target change. Expect the current gate to fail often on multi-paper topics.
- Either way, Product PRD §8 states the chosen definition explicitly.
