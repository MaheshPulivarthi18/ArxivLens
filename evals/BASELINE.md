# Eval baseline: extractive `/v1/ask` (no LLM), CI CPU profile

This is the reference that "`make eval` doesn't regress" compares against. Every value below is copied exactly from the
job log of the first green `main` run. Where the log prints a metric twice at different precision, the gate-table value
is used and the summary value is given next to it.

| | |
|---|---|
| Run | [`eval.yml` run 43902662193](https://github.com/MaheshPulivarthi18/ArxivLens/actions/runs/43902662193) (nightly schedule), job 133423130747 |
| `main` SHA | `40dec5a` (`40dec5a41ddeab7b029d5a03d5b6d66fbde1f411`) |
| Date | 2026-11-16 (job 16:00–17:02 UTC) |
| Results JSON | `evals/results/20261116T170152Z.json` in artifact `eval-results-43902662193` (ID 14247998742, kept 30 days) |
| Result | `GATE: PASS` |

## Profile: CPU-only
This is the **CPU-only** profile, so `/v1/ask` p95 is informational here: the gate prints it as
`INFO (cpu-only; not gated)` and it can neither pass nor fail the run (Technical PRD §9, Accepted 2026-11-15). The
`< 3000 ms` latency gate applies only on the GPU reference profile (`mps`/`cuda`), and this file has no GPU baseline.

- Runner: GitHub `ubuntu-latest` (image `ubuntu-26.04` 20261111.196.1), 4 CPUs (AMD EPYC 9V45), 15 GB RAM.
- PyTorch `2.15.0+cpu`. Embedder `/healthz`: `{"status":"ok","device":"cpu","reranker":true,"verifier":true}`; the gate logged
  `hardware: embedder device=cpu profile=cpu-only`.
- No `LLM_MODEL` set, so every `/v1/ask` answer is extractive and `insufficient_evidence` (LLM only) can't occur.
- arXiv was replayed from the Actions cache (`scripts/arxiv_replay.py`, #45); the corpus was seeded by `make eval-seed`.

Live ordering in this run: RRF candidate pool of 20 reranked by the cross-encoder (ADR-0010), reranker and first-stage
ranks fused (ADR-0017), at most two chunks per paper in the top 5 (ADR-0016). Answers quote the top passages, and each
quote is verified by quote match or NLI entailment (#44).

## Gate table
| Metric | Target | Baseline | Result |
|---|---|---|---|
| rerank via production `/internal/rerank` | == 1.0 | 1.0000 | PASS |
| retrieval_coverage | == 1.0 | 1.0000 (78/78 cases) | PASS |
| Recall@5, paper-level (ADR-0006), Hybrid RRF + Rerank | >= 0.8 | 0.9850 | PASS |
| hybrid+rerank beats BM25 & dense (MRR, NDCG@5) | == 1.0 | 1.0000 | PASS |
| supported_citation_rate (answer set) | >= 0.9 | 1.0000 (summary: 100.00%, 60 citations) | PASS |
| correct_refusal_rate | >= 0.95 | 1.0000 (summary: 100.00% of 22) | PASS |
| `/v1/ask` ask_p95_ms | < 3000 | 13330.2916 (summary: 13330) | INFO (cpu-only; not gated) |
| ask_errors (answer + refusal sets) | == 0 | 0.0000 | PASS |
| `/v1/search` p95 | < 1500 (PRD §8) | not run: the gate doesn't measure it yet (EVAL-2, ADR-0012 Proposed) | — |
| (info, not gated) chunk_precision@5, Hybrid RRF + Rerank | — | 0.6333 | — |
| (info) expected_paper_hit_rate | — | 0.9545 (summary: answers citing expected 95.45% of 22) | — |
| (info) in-corpus refused (answer set) | — | 4.55% (summary line; 1 of 22) | — |
| (info) ask_p50_ms | — | 10932.2 (summary: 10932) | — |
| (report-only) citation_coverage | — | 1.0000 (summary: 100.00%) | — |
| (report-only) near_miss_correct_refusal_rate | — | 0.8000 (summary: 80.00% of 15) | — |
| (report-only) verification_unavailable_count | — | 0 | — |
| (report-only) refusal_reason_counts | — | `{}` | — |

`refusal_reason_counts {}` means no response carried a `refusal_reason`, so the answer-set refusal above is an early
refusal (no `refusal_reason`, `citations: []`, see `evals/README.md`). "1 of 22" is derived: 4.55% of 22 cases.

## Retrieval summary (paper-level Recall@5 / MRR / NDCG@5; ChunkP@5 is diagnostic)
| Configuration | Recall@5 | MRR | NDCG@5 | ChunkP@5 | Avg ms |
|---|---|---|---|---|---|
| BM25 Keyword | 0.4573 | 0.4615 | 0.4520 | 0.3051 | 1.22 |
| Dense Vector | 0.9060 | 0.9391 | 0.9080 | 0.8410 | 0.98 |
| Hybrid RRF | 0.9231 | 0.9615 | 0.9319 | 0.8513 | 3.81 |
| Hybrid RRF + Rerank | 0.9850 | 0.9765 | 0.9707 | 0.6333 | 10264.58 |

## MRR per query kind (info, not gated)
| Kind | n | BM25 Keyword | Dense Vector | Hybrid RRF | Hybrid RRF + Rerank |
|---|---|---|---|---|---|
| abstract-paraphrase | 34 | 0.8382 | 1.0000 | 1.0000 | 1.0000 |
| body-detail | 24 | 0.1250 | 0.8750 | 0.9167 | 0.9514 |
| own-words | 6 | 0.0000 | 0.8333 | 0.8333 | 0.8889 |
| siblings | 8 | 0.1875 | 0.9062 | 1.0000 | 1.0000 |
| multi | 6 | 0.5000 | 1.0000 | 1.0000 | 1.0000 |

GPU-profile latency on Mahesh's reference Mac is in the PR descriptions of #46–#50 (reported, not re-measured; no
run artifact), so it is not a baseline value here.

## Commands for the human (repo root)
```bash
make setup-py                                         # host Python deps (CPU torch is fine)
make build-go && make stack-up && make migrate        # infra + processor, embedder worker, embedder API (:8081)
./bin/arxivlens query --port 8080 &                   # query service on :8080
make eval-seed                                        # ingest every ID in evals/corpus.txt (see evals/README.md)
make eval                                             # or: python3 scripts/eval_gate.py
```
Or trigger `eval.yml` (Actions → Eval → Run workflow): it runs the same steps, prints the gate table in the job log
and uploads `evals/results/*.json` as the run artifact. To re-baseline, copy the new run's numbers from its job log
into this file with the run URL, job ID, `main` SHA and profile.
