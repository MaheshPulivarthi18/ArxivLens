# evals/ — golden eval sets and quality gate (EVAL-1)

Quality targets come from Product PRD §8 (in [arxivlens-agents](https://github.com/MaheshPulivarthi18/arxivlens-agents/blob/main/docs/PRD-Product.md)) and live in `thresholds.json`. **Never lower a threshold or delete a
case to make a run pass**; a wrong threshold needs an Accepted ADR.

| File | Schema | Min size |
|---|---|---|
| `corpus.txt` | one arXiv ID per line (no version), `#` comments | — |
| `retrieval.jsonl` | `{"id","query","relevant_arxiv_ids":[...]}` | 30 |
| `answer.jsonl` | `{"id","question","expected_arxiv_ids":[...]}` (in-corpus) | 20 |
| `refusal.jsonl` | `{"id","question"}` (clearly out of corpus) | 20 |
| `thresholds.json` | PRD §8 targets used by `scripts/eval_gate.py` | — |
| `results/` | one JSON per gate run (git-ignored) | — |

## Running
```bash
make infra-up && make migrate            # Postgres+pgvector, Kafka, MinIO
# start the embedder API (:8081) and the query service (:8080), then seed the corpus (below)
make eval                                # = python3 scripts/eval_gate.py; exit 0 pass, 1 fail, 2 could not run
python3 scripts/evaluate_retrieval.py    # retrieval only (DATABASE_URL + ARXIVLENS_EMBEDDER_URL, default http://localhost:8081)
python3 scripts/evaluate_answers.py      # /v1/ask only (ARXIVLENS_API_URL, default http://localhost:8080)
```
### What the retrieval gate measures
Decisions: [ADR-0006](https://github.com/MaheshPulivarthi18/arxivlens-agents/blob/main/docs/adr/0006-recall-at-5-definition.md)
(metric definition) and [ADR-0010](https://github.com/MaheshPulivarthi18/arxivlens-agents/blob/main/docs/adr/0010-ask-sorts-by-rerank.md)
(ordering), both in arxivlens-agents.

- **Recall@5 (gated, ≥ 0.80) is paper-level.** Each mode returns 5 chunks (the `/v1/ask` default `maxCitations`). They
  are deduped to arXiv IDs in rank order, and Recall@5 = `|top-5 papers ∩ relevant_arxiv_ids| / |relevant_arxiv_ids|`.
  MRR and NDCG@5 use the same paper ranking. Extra chunks from a paper that is already counted do not raise any of them.
- **`chunk_precision@5` is a diagnostic and is not gated:** `|top-5 chunks from a relevant paper| / 5`. It is printed
  and saved in the results JSON.
- **Gated mode "Hybrid RRF + Rerank" follows ADR-0010.** The RRF candidate pool of 20 uses the same SQL as
  `internal/query/search.go` (50 per arm, `ce.model` filter, k = 60). The whole pool is reranked in one
  `POST /internal/rerank` call to the embedder API's cross-encoder, then stable-sorted by score (ties keep RRF order)
  and cut to the top 5. **Production `/v1/ask` returns this ordering only once RET-1 merges.** Until then, the gate
  measures the ADR-0010 target behaviour, not today's `/v1/ask`, which reranks only its RRF top 5 and does not sort.
  The eval refuses to run if `/healthz` reports `"reranker": false`, because the endpoint would then fall back to
  bi-encoder scores. A rerank error fails the run; it does not fall back to RRF order. The gate also fails unless the
  results record the production rerank path.
- The baselines (BM25, dense, RRF without rerank) are scored the same way on their top 5 chunks. Dense and RRF only
  use embeddings for the current `EMBEDDING_MODEL`.
- **Model name: two sources that must match.** The eval reads `EMBEDDING_MODEL` (`services/common/config.py`,
  default `BAAI/bge-base-en-v1.5`) and records it as `embedding_model` in the results JSON. The Go query service
  takes its model from its `--model` flag. If they differ, the eval scores different vectors than production
  serves. The eval compares `EMBEDDING_MODEL` with the query service's model: `--query-model` /
  `ARXIVLENS_QUERY_MODEL` if you started the service with `--model`, otherwise the `--model` default read from
  `cmd/arxivlens/main.go`. It records that value as `query_model` and prints a warning when the two differ; the
  warning does not fail the gate. A unit test fails if the Go `--model` default and the `EMBEDDING_MODEL` default
  in `config.py` drift apart.
- **Partly embedded papers are reported, not gated.** For every relevant paper the results JSON records
  `chunk_coverage[arxiv_id] = {embedded_chunks, total_chunks}` for the current model, and the run prints a warning
  for each paper with `embedded_chunks < total_chunks`. Such a paper still counts as indexed (making it fail the
  gate needs an ADR).
- The SQL parity test in `tests/test_evals_data.py` compares the RRF pool query with `internal/query/search.go`
  (whole normalised query, explicit list of allowed differences, `RerankPool` = pool size) and fails if that file
  is missing.

Coverage is checked per arXiv ID. A paper counts as indexed only if it has chunks **with embeddings for the current
model** (`chunk_embeddings.model`). A case is skipped unless every one of its `relevant_arxiv_ids` is indexed,
and the missing IDs are printed and saved under `missing_arxiv_ids`. The gate fails if any case is skipped (coverage
must be 1.0), and a partial run (`--skip-retrieval` / `--skip-answers`) never passes.
Every failed `/v1/ask` call (HTTP error, timeout, bad JSON) fails the gate via the `ask_errors == 0` check. It is
also recorded in the p50/p95 latency at its elapsed time (a timeout counts as at least `--timeout`), and never counts
as a refusal, hit, or supported citation. If every call fails, the gate exits 2 (could not run).

## Seeding the corpus
On-demand ingest via the query API (fetches from arXiv, chunks and embeds):
```bash
grep -oE '^[0-9]{4}\.[0-9]{4,5}' evals/corpus.txt | while read id; do
  curl -s -X POST localhost:8080/v1/papers/ingest -H 'Content-Type: application/json' -d "{\"arxiv_id\":\"$id\"}"; echo
  sleep 3   # arXiv politeness: <= 1 request / 3 s
done
```
Or, for papers already downloaded by the crawler/downloader pipeline: `python3 scripts/ingest_batch.py`.

## Adding cases
1. Use a real arXiv ID you are sure matches the query; add it to `corpus.txt` first.
2. Give each case a new unique `id` (`rNNN`, `aNNN`, `xNNN`); don't renumber or delete existing ones.
3. Retrieval queries should describe the paper's content, not just quote its title. Refusal questions must be
   clearly outside the corpus (cooking, sports, geography, ...), not adjacent ML topics.
4. Run `python3 -m pytest tests/test_evals_data.py -q` (schema, sizes, corpus membership, unique IDs).
5. Re-run `make eval` and put before/after numbers in the PR.
