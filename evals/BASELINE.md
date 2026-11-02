# Eval baseline — templated `/v1/ask` (pre-QA-8)

**Status: not run: infra unavailable in agent sandbox** (2026-11-01, EVAL-1; definitions updated 2026-11-02). Docker Hub, Hugging Face, arXiv and the
Go 1.25 toolchain are unreachable there, so no stack, model, or corpus could be brought up. No numbers are claimed.

## Commands for the human (repo root)
```bash
make infra-up && make migrate
python3 services/embedder/api.py &                    # :8081
make build-go && ./bin/arxivlens query --port 8080 &   # query service on :8080
# seed every ID in evals/corpus.txt — see evals/README.md "Seeding the corpus"
make eval                                             # or: python3 scripts/eval_gate.py
```
The gated mode follows ADR-0010 (RRF pool of 20, reranked by the cross-encoder, top 5). Until RET-1 merges, production
`/v1/ask` does not order results that way, so this baseline measures the target retrieval ordering. Re-record it after
RET-1 merges and note which ordering was live.

Then paste the printed table and the `evals/results/<timestamp>.json` path below and commit this file.

| Metric | Target (PRD §8) | Baseline |
|---|---|---|
| Recall@5, paper-level (ADR-0006), Hybrid RRF + Rerank | ≥ 0.80 | not run |
| (info, not gated) chunk_precision@5, Hybrid RRF + Rerank | — | not run |
| Hybrid+rerank beats BM25 and dense on MRR, NDCG@5 | yes | not run |
| Supported citations | ≥ 90% | not run |
| Correct refusals | ≥ 95% | not run |
| `/v1/ask` p95 | < 3000 ms | not run |
| (info) answers citing an expected paper | — | not run |
