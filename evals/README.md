# evals/ — golden eval sets and quality gate (EVAL-1)

Quality targets come from Product PRD §8 (in [arxivlens-agents](https://github.com/MaheshPulivarthi18/arxivlens-agents/blob/main/docs/PRD-Product.md)) and live in `thresholds.json`. **Never lower a threshold or delete a
case to make a run pass**; a wrong threshold needs an Accepted ADR.

| File | Schema | Min size |
|---|---|---|
| `corpus.txt` | one arXiv ID per line (no version), `#` comments | — |
| `retrieval.jsonl` | `{"id","kind","query","relevant_arxiv_ids":[...]}` (kinds below) | 30, ≥ 6 per kind |
| `answer.jsonl` | `{"id","question","expected_arxiv_ids":[...]}` (in-corpus) | 20 |
| `refusal.jsonl` | `{"id","question"}` (clearly out of corpus) | 20 |
| `refusal_near_miss.jsonl` | `{"id","question","answered_by","why_near","reviewed_by"}` (in-domain, answering paper not in corpus; report-only) | 10 |
| `thresholds.json` | PRD §8 targets used by `scripts/eval_gate.py` | — |
| `results/` | one JSON per gate run (git-ignored) | — |
| `results/` → `llm` | which LLM answered (EVAL-8c, report-only, never gated): `model` (`LLM_MODEL` or `null`), `source: "env"`, `host` (host[:port] of `LLM_BASE_URL`, default `localhost:11434` only with a model; never the scheme, path, query, userinfo or key), `ollama_version`, `digest`, `digest_short` (first 12 hex, = `ollama list` ID) from `GET /api/version` and `/api/tags` (bearer `LLM_API_KEY`, 5 s, not called without `LLM_MODEL`; on failure `null` plus `probe_error`), `synthesized_answer_rate` (answered full responses with `answer_kind: synthesized` / all answered, every set; `null` if none answered) | — |
| `results/` → `answers.cases[]` | adds `answer_kind` (`quoted`, `synthesized` or `null`) and each citation's ADR-0014 `status` (or `null`) | — |
| `answer_judgments/` | `<run-id>.jsonl` human review labels, IDs only (EVAL-7, see Human review) | — |

## Running
```bash
make infra-up && make migrate            # Postgres+pgvector, Kafka, MinIO
# start the embedder API (:8081) and the query service (:8080), then seed the corpus (below)
make eval                                # = python3 scripts/eval_gate.py; exit 0 pass, 1 fail, 2 could not run
python3 scripts/evaluate_retrieval.py    # retrieval only (DATABASE_URL + ARXIVLENS_EMBEDDER_URL, default http://localhost:8081)
python3 scripts/evaluate_answers.py      # /v1/ask only (ARXIVLENS_API_URL, default http://localhost:8080)
```
In CI, `.github/workflows/eval.yml` runs the full stack, `make eval-seed` and `make eval` nightly (`schedule`) and on
demand (`workflow_dispatch`: Actions → Eval → Run workflow). The gate table is printed in the job log, the results JSON
is uploaded as the run artifact `eval-results-<run_id>`, and arXiv is replayed from the Actions cache (#45). The
reference numbers are in `BASELINE.md`.
### Retrieval query kinds
The first 34 cases paraphrase abstracts with their distinctive keywords, and each targets a paper whose topic no other
corpus paper shares. Every mode scored near 1.0 on them, so "hybrid + rerank beats BM25 and dense" depended on a single
query's rank and flipped between machines. Cases h001–h044 are written the way scholars search a library they know:

| `kind` | What it tests | Example |
|---|---|---|
| `abstract-paraphrase` | the original cases (r001–r034) | "masked language model pre-training of deep bidirectional transformers" |
| `body-detail` | a fact from the method or experiments, not the abstract | "why are dot-product attention scores divided by the square root of the key dimension" |
| `own-words` | the reader's vocabulary, not the paper's | "how to find the closest vectors quickly in a very large collection" |
| `siblings` | picking the right paper among close neighbours (BERT/RoBERTa, Kaplan/Chinchilla, DDPM/LDM, InstructGPT/DPO) | "the original BERT was significantly undertrained" |
| `multi` | questions that two or three corpus papers answer; recall needs all of them in the top 5 | "sinusoidal versus rotary encodings of token positions in transformers" |

Every label was checked against the indexed text. Where another corpus paper legitimately answered a draft query
(T5 and ViT on position encodings, BEIR on dense retrieval), the query was narrowed rather than the label widened.
The per-kind MRR table is printed and saved as `by_kind` in the results JSON; it is information, not a gate.

### What the retrieval gate measures
Decisions: [ADR-0006](https://github.com/MaheshPulivarthi18/arxivlens-agents/blob/main/docs/adr/0006-recall-at-5-definition.md)
(metric definition), [ADR-0010](https://github.com/MaheshPulivarthi18/arxivlens-agents/blob/main/docs/adr/0010-ask-sorts-by-rerank.md),
[ADR-0017](https://github.com/MaheshPulivarthi18/arxivlens-agents/blob/main/docs/adr/0017-rerank-rank-fusion.md) and
[ADR-0016](https://github.com/MaheshPulivarthi18/arxivlens-agents/blob/main/docs/adr/0016-top5-paper-diversity.md)
(ordering), all in arxivlens-agents.

- **Recall@5 (gated, ≥ 0.80) is paper-level.** Each mode returns 5 chunks (the `/v1/ask` default `maxCitations`). They
  are deduped to arXiv IDs in rank order, and Recall@5 = `|top-5 papers ∩ relevant_arxiv_ids| / |relevant_arxiv_ids|`.
  MRR and NDCG@5 use the same paper ranking. Extra chunks from a paper that is already counted do not raise any of them.
- **`chunk_precision@5` is a diagnostic and is not gated:** `|top-5 chunks from a relevant paper| / 5`. It is printed
  and saved in the results JSON.
- **Gated mode "Hybrid RRF + Rerank" follows ADR-0010, ADR-0017 and ADR-0016.** The RRF candidate pool of 20 uses the
  same SQL as `internal/query/search.go` (50 per arm, `ce.model` filter, k = 60). The whole pool is reranked in one
  `POST /internal/rerank` call to the embedder API's cross-encoder, ordered by fusing rerank and RRF ranks (ADR-0017:
  `1/(10 + rerank rank) + 1/(10 + RRF rank)`, ties keep RRF order), limited to two chunks per paper in the top 5
  (ADR-0016), and cut to the top 5. This is the ordering production `/v1/ask` returns (RET-1 merged in `bb9ee90`).
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

**Latency is gated on the reference hardware only** (Technical PRD §9, Accepted 2026-11-15). The gate reads the
embedder's device from `GET /healthz`. On `mps` or `cuda` (the GPU profile self-hosters run) `ask_p95_ms < 3000` is
gated. On `cpu`, such as a GitHub runner, the p95 is still measured, saved and printed, labelled
`INFO (cpu-only; not gated)`, and it can neither pass nor fail the run. If the device can't be read, the latency gate
applies. Every quality check is gated on every machine, and so is a missing p95.

**The results say which LLM answered** (EVAL-8c). The gate prints an `llm:` line next to `hardware:` and stores the
`llm` block above. It only reports: no check reads it and the exit code never depends on it. It warns on stderr when
answers were synthesized but `LLM_MODEL` is unset in the eval's env (export it for `make eval` too), or when `LLM_MODEL`
is set but no answer was synthesized (every answer fell back to quoting).

**The gated latency is the quick stage** (ADR-0019). For each answer- and refusal-set question the eval also calls
`/v1/ask` with `"quick": true`, the quoted answer the reader sees first, and `ask_p50_ms` / `ask_p95_ms` time those
calls (`latency_stage: "quick"`); a failed quick call is an ask error. Every quality metric comes from the full answer,
whose latency is the report-only `ask_full_p50_ms` / `ask_full_p95_ms`. With an LLM configured that is the written
answer, which on a laptop model takes seconds and is shown when ready.

**`/v1/search` latency is report-only** (B-QUAL; `scripts/evaluate_search.py`). After the answers phase, against the same
`--api-url`, the gate makes one uncounted warm-up call and then times `POST /v1/search` with `{"query", "limit": 5,
"rerank": true}` (the production path, ADR-0010) once per `retrieval.jsonl` query. `results["search"]` gets
`search_p50_ms` / `search_p95_ms` (nearest rank, successful calls only; `null` if none succeeded), `search_count` and
`search_error_count` (transport error, non-2xx or bad JSON; if measuring itself fails, every call counts). The summary
prints `/v1/search p50/p95 (ms): a / b (report-only, ADR-0012 Proposed; target 1500) errors=N/M profile=...`. Nothing
is gated: Product PRD §8's 1.5 s target has no key in `thresholds.json` until ADR-0012 is decided, so no check reads
these keys and the exit code never depends on them. `--skip-search` (or `--skip-answers`) skips it. When ADR-0012 is
Accepted, the follow-up is the threshold key, one `_check` in `check_thresholds` and an entry in `LATENCY_GATES`.

### What the answer eval measures (EVAL-6)
**ADR-0003 classification** of each `/v1/ask` response (`classify()` in `scripts/evaluate_answers.py`):

| Response | Outcome | Refusal set | Answer set |
|---|---|---|---|
| request failed (HTTP error, timeout, bad JSON) | error | `ask_error_count` +1, not refused | `ask_error_count` +1 |
| malformed response (body not an object, `citations` not a list, a non-object citation, a non-string citation `arxiv_id`) | **error** | `ask_error_count` +1, not refused | `ask_error_count` +1 |
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
  `eq.` (a test checks that list against `rag.go`). The value is `null` when there are no answered responses. Malformed
  `claims` (an item that isn't an object, `markers` that isn't a list of integers) count as uncited units, and a
  citation `marker` that isn't an integer is ignored; neither changes the outcome.
- **`refusal_reason_counts`**: how many responses carried each `refusal_reason` value, over all three sets. String
  values only: a non-string `refusal_reason` (e.g. a list or a number) is not counted here; it counts as an error.
- **`verification_unavailable_count`**: ADR-0003 case B responses in the answer and refusal sets (also in `ask_error_count`).
- **`near_miss_correct_refusal_rate`** = refused / all rows of `refusal_near_miss.jsonl`, classified as above (errors
  and B count as not refused); `null` when the set is empty. Near-miss calls are kept out of the gated latency and
  `ask_error_count`, so an unreviewed row can't change a gate.
- **Per-case records** (`answers.cases`): `id`, `set`, `outcome`, `refused`, `refusal_reason`, `latency_ms`,
  `cited_units`/`total_units` (answered only), and per citation `arxiv_id`, `chunk_id`, `marker`, `supported`, `score`.
  **No question, passage or answer text is saved**, because CI artifacts of this public repo can be downloaded.
- **Summary IDs** (EVAL-10): the printed `/v1/ask` summary in the job log lists the case IDs behind the misses, as
  `refused IDs` (`answer_refused_ids`), `miss IDs` (`expected_paper_miss_ids`: refusals, errored calls and
  wrong-paper answers) and `near-miss answered IDs` (`near_miss_failure_ids`, errors included). Each line is printed only when it has IDs, and it holds IDs only, no text.

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

## Section-extraction eval (ING-8)
Measures how often the **production** section detector (`services/processor/worker.py`
`ProcessorWorker._parse_doc_sections`, Technical PRD §6.1) finds the real headings of a paper. Every chunk is prefixed
with its section title, so this measures part of chunk quality. It is a measurement only: it has no gate and is not in
`thresholds.json`. Baseline: [`EXTRACTION_BASELINE.md`](EXTRACTION_BASELINE.md).

| File | Schema | Min size |
|---|---|---|
| `extraction.jsonl` | `{"arxiv_id","version","license","layout","expected_headings":[{"title","level"}...]}` | 10 rows, ≥ 3 layouts |

```bash
pip install pymupdf numpy   # plus what services/processor/worker.py imports (requirements-m0.txt, confluent-kafka)
python3 scripts/evaluate_extraction.py            # fetches PDFs, writes evals/results/extraction-<ts>.json, prints a table
python3 scripts/evaluate_extraction.py --offline  # re-score cached PDFs only, no network
python3 -m pytest -q tests/test_evaluate_extraction.py   # no network, no PDFs
```
Exit code 0 means every paper was scored. Exit code 2 means the eval could not run, or some paper failed to fetch or
parse (see `error` in the output). A failed paper counts with zero predicted headings, so it lowers micro recall
instead of disappearing from the numbers.

**Licence and PDF rules.**
- Only papers whose arXiv licence (checked on the abs page) is `CC BY 4.0`, `CC BY-SA 4.0` or `CC0 1.0`. Record it in `license`.
- Pin `version` (`v1`, `v2`, ...) because headings can change between versions.
- PDFs are fetched at run time from `https://arxiv.org/pdf/<id><version>`, with at most one request every 3 s and an
  identifying User-Agent (Technical PRD §9). They are cached in `.cache/extraction/` (git-ignored). The script refuses
  a cache dir under `evals/`. **Never commit a PDF.**

**Annotating.** Write `expected_headings` by hand from the PDF, in document order, including appendix headings. Use
level 1 for sections and 2 for subsections. Copy titles as printed. Numbering is optional because it gets normalised
away. Never generate labels with the detector being measured.

**Scoring rules.**
1. Normalise a title: strip leading numbering (`^\s*(\d+(\.\d+)*|[IVXLCDM]+|[A-Z])[.)]?\s+`, applied before
   lower-casing so Roman numerals and appendix letters are recognised), lower-case, collapse whitespace, strip
   trailing punctuation. `"3.1 Results."` becomes `results`. A side effect is that a title starting with a single-letter
   word (`"A Study of X"`) loses that word. Both lists are normalised the same way, so matching is unaffected.
2. `Abstract` is excluded from both lists. The detector puts text before the first heading into an implicit `Abstract`
   section (PRD §6.1 rule 4).
3. Walk the predicted headings in order. Match each one greedily to the first unmatched expected heading with an equal
   normalised title after the last match, so order is preserved and an out-of-order duplicate doesn't match. TP = matched.
4. **Boundary precision** = TP / predicted (0 if nothing is predicted). **Boundary recall** = TP / expected.
   F1 = 2PR / (P + R). These are reported per paper and micro-averaged (summed TP, predicted and expected).
   Level accuracy over the TPs is informational only.
5. Each paper lists its `false_positives` and `missed` titles, so follow-ups can target them.

"Predicted" means the sections the detector **emits**, which become chunk prefixes, not every line it classifies as a
heading. The detector drops a section that has no body text, such as a section heading followed directly by its
first subsection. Such a heading counts as missed.
