# EVAL-2: Eval the production reranker and gate `/v1/search` p95

| | |
|---|---|
| **Owner** | ai-engineer |
| **Priority** | P2 |
| **PRD ref** | Product PRD §8 (MRR/NDCG hybrid+rerank, `/v1/search` p95 < 1.5 s); QA-2 |
| **Depends on** | EVAL-1 (PR #2 merged) |
| **Status** | Ready (start once PR #2 is merged) |

## Goal
Two gaps that EVAL-1 found make the retrieval gate unrepresentative of production:
(a) the "Hybrid RRF + Rerank" mode in `scripts/evaluate_retrieval.py` reranks with a BGE bi-encoder dot product, but
`/v1/search` and `/v1/ask` use the cross-encoder behind `POST /internal/rerank` (bge-reranker-base); and (b) the PRD
target `/v1/search` p95 < 1.5 s is not measured or gated at all.

## Scope
- `scripts/evaluate_retrieval.py`: rerank mode calls the embedder API's `/internal/rerank` (URL from env, same default as the query service). Use the same candidate count as the query service. Keep the bi-encoder mode only if it is renamed (e.g. "Hybrid RRF + bi-encoder"), and do not gate on it.
- New `tests/test_eval_gate.py` (no infra): rerank-mode HTTP mock and gate-decision tests.
- `scripts/evaluate_answers.py` or a small new `scripts/evaluate_search.py` called by `scripts/eval_gate.py`: time `POST /v1/search` (`rerank: true`) for every `evals/retrieval.jsonl` query, after one warm-up call, and report p50/p95.
- `evals/thresholds.json`: add a **new gate key** `search_p95_latency_ms_max: 1500`. The value is the existing Product PRD §8 target, but adding a gate key changes the gate. Thresholds change only through an Accepted ADR, so **the PR needs Mahesh's explicit confirmation** of this key before merge (ask in the PR description).
- Results JSON records the machine profile (CPU model, core count, RAM, torch device: `mps`/`cuda`/`cpu`), so numbers can be read against Technical PRD §9.
- Update `evals/README.md` and `evals/BASELINE.md`.
- Out of scope: changing any existing threshold, the Recall@5 definition (ADR-0006), or `rag.go`.

## Acceptance criteria
- [ ] AC1 — The gated rerank mode's scores come from `/internal/rerank`, shown by a unit test in `tests/test_eval_gate.py` with a mocked HTTP endpoint (no infra).
- [ ] AC2 — `make eval` prints `/v1/search` p50/p95 and exits 1 when p95 > 1500 ms, **unconditionally** (on any machine, same as today's `ask_p95` gate). The machine profile is written to the results JSON. A unit test in `tests/test_eval_gate.py` covers the gate decision with fixed numbers. If Mahesh approves the Proposed reference-hardware rule in Technical PRD §9, a later task makes latency gates profile-aware; do not implement that here.
- [ ] AC3 — Failed `/v1/search` calls fail the gate, the same way `ask_errors` does.
- [ ] AC4 — The PR description asks Mahesh to confirm the new `search_p95_latency_ms_max` key.
- [ ] AC5 — Before/after `make eval` numbers pasted, or "not run: infra unavailable" plus the exact command.

## Verification
```bash
make test-unit
python3 -m pytest -q tests/test_evals_data.py
python3 -m pytest -q tests/test_eval_gate.py
python3 scripts/eval_gate.py --help
make eval   # when infra is available
```

## Notes
- Specced by pm-lead in PM-1 from EVAL-1 findings (a) and (d).
