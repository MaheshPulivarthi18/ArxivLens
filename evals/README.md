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
