# evals/ — golden eval sets and quality gate (EVAL-1)

Quality targets come from Product PRD §8 (in [arxivlens-agents](https://github.com/MaheshPulivarthi18/arxivlens-agents/blob/main/docs/PRD-Product.md)) and live in `thresholds.json`. **Never lower a threshold or delete a
case to make a run pass**; a wrong threshold needs an Accepted ADR.

| File | Schema | Min size |
|---|---|---|
| `corpus.txt` | one arXiv ID per line (no version), `#` comments | — |
| `retrieval.jsonl` | `{"id","query","relevant_arxiv_ids":[...]}` | 30 |
| `answer.jsonl` | `{"id","question","expected_arxiv_ids":[...]}` (in-corpus) | 20 |
| `refusal.jsonl` | `{"id","question"}` (clearly out of corpus) | 20 |
| `refusal_near_miss.jsonl` | `{"id","question","answered_by","why_near","reviewed_by"}` (in-domain, answering paper not in corpus; report-only) | 10 |
| `thresholds.json` | PRD §8 targets used by `scripts/eval_gate.py` | — |
| `results/` | one JSON per gate run (git-ignored) | — |
| `answer_judgments/` | `<run-id>.jsonl` human review labels, IDs only (EVAL-7, see Human review) | — |

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
  is missing. It rebuilds the production query from the Go string constants (`hybridSearchSQL` with
  `vectorHitsSQL` spliced in), so comments are ignored. The eval also runs production's `hnswSearchSettings`
  (`hnsw.ef_search = 100`, `iterative_scan = strict_order`, pgvector >= 0.8.0) right before the pool query and
  resets them after it; the test checks the settings text matches too.

Coverage is checked per arXiv ID. A paper counts as indexed only if it has chunks **with embeddings for the current
model** (`chunk_embeddings.model`). A case is skipped unless every one of its `relevant_arxiv_ids` is indexed,
and the missing IDs are printed and saved under `missing_arxiv_ids`. The gate fails if any case is skipped (coverage
must be 1.0), and a partial run (`--skip-retrieval` / `--skip-answers`) never passes.
Every failed `/v1/ask` call (HTTP error, timeout, bad JSON) fails the gate via the `ask_errors == 0` check. It is
also recorded in the p50/p95 latency at its elapsed time (a timeout counts as at least `--timeout`), and never counts
as a refusal, hit, or supported citation. If every call fails, the gate exits 2 (could not run).

### What the answer eval measures (EVAL-6)
**ADR-0003 classification** of each `/v1/ask` response (`classify()` in `scripts/evaluate_answers.py`):

| Response | Outcome | Refusal set | Answer set |
|---|---|---|---|
| request failed (HTTP error, timeout, bad JSON) | error | `ask_error_count` +1, not refused | `ask_error_count` +1 |
| no `refusal_reason`, `citations: []` (early refusal, QA-5) | refusal | correct refusal | `answer_refusal_rate` |
| `refusal_reason: "all_citations_unsupported"` (case A, carries evidence) | refusal | correct refusal | `answer_refusal_rate` |
| `refusal_reason: "verification_unavailable"` (case B) | **error** | `ask_error_count` and `verification_unavailable_count` +1, not refused | same |
| any other `refusal_reason` (fail closed) | error | `ask_error_count` +1, not refused | same |
| no `refusal_reason`, ≥ 1 citation | answered | false answer | scored below |

- **`supported_citation_rate` (gated)** = supported / all citations of *answered* answer-set responses. A refusal's
  evidence passages are not answer citations and are not counted (pm-lead's reading of ADR-0003, to be confirmed by
  Mahesh). An A-refusal is also never an expected-paper hit.

**Report-only metrics.** They are printed and saved in the results JSON, and `check_thresholds` never reads them.
Promoting one to a gate needs an ADR and a key in `thresholds.json`.
- **`citation_coverage`** = cited units / all units, summed over answered answer-set responses. A unit is a `claims[]`
  item (ADR-0014) when the response has a non-empty `claims` list, otherwise an answer sentence. A unit is cited if it
  has ≥ 1 in-range marker, meaning a `[n]` (or a `claims[].markers` entry) that matches the `marker` of a returned
  citation. The server drops markers outside 1..K, so only in-range markers get a citation. Sentences come from
  `split_sentences()`, a port of `splitIntoSentences` in `internal/query/rag.go`: a sentence ends at `.`, `!` or `?`
  followed by a space or the end of the text, unless it ends with `e.g.`, `i.e.`, `et al.`, `cf.`, `vs.`, `fig.` or
  `eq.` (a test checks that list against `rag.go`). The value is `null` when there are no answered responses.
- **`refusal_reason_counts`**: how many responses carried each `refusal_reason` value, over all three sets.
- **`verification_unavailable_count`**: ADR-0003 case B responses in the answer and refusal sets (also in `ask_error_count`).
- **`near_miss_correct_refusal_rate`** = refused / all rows of `refusal_near_miss.jsonl`, classified as above (errors
  and B count as not refused); `null` when the set is empty. Near-miss calls are kept out of the gated latency and
  `ask_error_count`, so an unreviewed row can't change a gate.
- **Per-case records** (`answers.cases`): `id`, `set`, `outcome`, `refused`, `refusal_reason`, `latency_ms`,
  `cited_units`/`total_units` (answered only), and per citation `arxiv_id`, `chunk_id`, `marker`, `supported`, `score`.
  **No question, passage or answer text is saved**, because CI artifacts of this public repo can be downloaded.

### Near-miss rows and human review
`refusal_near_miss.jsonl` holds in-domain (ML/IR) questions that sound like the corpus but are answered by a paper
that is **not** in `corpus.txt`: `answered_by` is that paper's arXiv ID, and `why_near` starts with the ID of the corpus
paper it resembles. A test fails if any `answered_by` is in `corpus.txt` or a field is missing. Rows are drafts until
a human reviewer (Mahesh or someone he names) checks on arxiv.org that `answered_by` answers the question and that no
corpus paper does, then sets `reviewed_by` to their name or handle. `reviewed_by: null` means not reviewed yet.
If a corpus paper turns out to answer a row, fix the row; don't add the paper to the corpus.

## Human review of answers (EVAL-7, report-only)
Cosine ≥ 0.50 says a sentence and a passage are related, not that the passage supports it. `scripts/answer_review.py`
lets a person check, and its numbers are the evidence for or against an entailment verifier (ADR-0014). Not gated.
**Who:** Mahesh or a reviewer he names. Agents never fill in judgments; tests use synthetic fixtures only.

- **Export** (query service up on the run's corpus): `python3 scripts/answer_review.py export --results
  evals/results/<run-id>.json [--db-url ...]`. Results hold no text, so each answered case is asked again for the claim
  text; a case refused now, or whose per-citation (marker, chunk_id, score, supported) differs from the results, is
  skipped as drifted. Passage text comes by `chunk_id` from that response, or Postgres with `--db-url`. It writes
  `evals/results/<run-id>.review.md` to read and `<run-id>.judgments.jsonl` to fill: one row per (claim or sentence,
  cited marker); uncited sentences get none.
- **Keep it blind:** the sheet hides `score`, `supported`, `status`; don't open that run's results or UI until done.
- **Labels:** `supports` = `yes | partial | no | contradicts`; `claim_correct` = `yes | no | unsure` (one per claim);
  `reviewer`; `date` (`YYYY-MM-DD`); optional `notes` (local only). `supports: null` = unjudged (counted, not rated).
- **How long (estimate):** a 20-case template run is ~40 rows, 20–40 min at 30–60 s a row (replace once measured).
- **Score:** `python3 scripts/answer_review.py score --judgments <file> --results evals/results/<run-id>.json` prints
  citation precision (`yes` / judged), partial and contradiction rates, claim correctness (`yes` / judged claims),
  judged/unjudged counts and the confusion matrix of human `supports` × system `supported`.
- **Where judgments go:** `--save evals/answer_judgments/` writes `<run-id>.jsonl` to commit (no overwrite without
  `--force`): IDs and labels only, never passage, answer or notes text (public repo; arXiv licences vary). A test
  rejects other keys and free text in ID fields. Keep the run's results JSON (or EVAL-3 artifact) for the matrix.

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
