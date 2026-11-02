# evals/ — golden eval sets and quality gate (EVAL-1)

Quality targets come from Product PRD §8 and live in `thresholds.json`. **Never lower a threshold or delete a
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
The gated retrieval mode, "Hybrid RRF + Rerank", reorders the Hybrid RRF top-10 by the production cross-encoder:
`POST /internal/rerank` on the embedder API, which is the same endpoint `/v1/ask` calls. The retrieval eval refuses to
run if the embedder API's `/healthz` reports `"reranker": false`, because in that state the endpoint falls back to
bi-encoder scores. The gate also fails unless the results record that production rerank path.
Coverage is checked per arXiv ID. A case is skipped unless every one of its `relevant_arxiv_ids` has indexed chunks,
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
