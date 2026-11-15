"""
Citation support verification by natural-language inference (NLI).

A citation is supported when its claim is *entailed* by the cited passage. Bi-encoder cosine similarity
(the previous check) measures topical closeness, not entailment: with BGE, a claim that contradicts the
passage, a sentence with no claim, and a true claim checked against a different paper's chunk all scored
>= 0.5 (0.50-0.83), so all were marked supported. An NLI cross-encoder separates them.

NLI models are trained on short premises, so a ~500-token chunk dilutes the evidence (1 of 4 true claims
passed when the whole chunk was the premise). The chunk is therefore split into windows of
WINDOW_SENTENCES consecutive sentences and the claim's support is the highest P(entailment) over the
windows (SummaC-style). On a hand-written probe this gave true claims >= 0.987 and contradictions,
claim-free sentences, unrelated sentences and other papers' chunks <= 0.051.
"""

import re
import time
from typing import List, Sequence, Tuple

import torch

DEFAULT_NLI_MODEL = "cross-encoder/nli-deberta-v3-small"
WINDOW_SENTENCES = 2
# Premise windows are a few sentences; 256 tokens covers premise + claim with room to spare.
NLI_MAX_LENGTH = 256
NLI_BATCH_SIZE = 16
# Sentence fragments shorter than this (page furniture, "Fig. 3", stray numbers) are not windows.
MIN_SENTENCE_CHARS = 20

_SENTENCE_END = re.compile(r"(?<=[.!?])\s+")


def strip_chunk_prefix(passage: str) -> str:
    """Drop the chunker's "<paper title> — <section>\\n\\n" prefix so a claim cannot be 'entailed' by the title."""
    head, sep, body = passage.partition("\n\n")
    if sep and " — " in head:
        return body  # may be empty: a prefix-only chunk has no premise text (never fall back to the title)
    return passage


def premise_windows(passage: str, k: int = WINDOW_SENTENCES) -> List[str]:
    """Windows of k consecutive sentences of the passage body; the whole body when it has fewer than k."""
    body = " ".join(strip_chunk_prefix(passage).split())
    sents = [s for s in _SENTENCE_END.split(body) if len(s) >= MIN_SENTENCE_CHARS]
    if len(sents) <= k:
        return [body] if body else []
    return [" ".join(sents[i:i + k]) for i in range(len(sents) - k + 1)]


def entailment_index(model) -> int:
    """Index of the 'entailment' label in the model's output."""
    labels = {str(v).lower(): int(k) for k, v in model.config.id2label.items()}
    if "entailment" not in labels:
        raise ValueError(f"model has no 'entailment' label: {model.config.id2label}")
    return labels["entailment"]


def verify_pairs(tokenizer, model, pairs: Sequence[Tuple[str, str]], entail_idx: int,
                 batch_size: int = NLI_BATCH_SIZE) -> Tuple[List[float], dict]:
    """Support score in [0, 1] for each (claim, passage): max P(entailment) over the passage's windows.

    A pair whose passage has no usable text scores 0. Returns (scores, stats); stats are counts and
    timings only, never text.
    """
    t0 = time.perf_counter()
    jobs = []  # (pair index, premise window, claim)
    for i, (claim, passage) in enumerate(pairs):
        for w in premise_windows(passage):
            jobs.append((i, w, claim))
    order = sorted(range(len(jobs)), key=lambda j: len(jobs[j][1]) + len(jobs[j][2]))
    scores = [0.0] * len(pairs)
    device = getattr(model, "device", None)
    with torch.inference_mode():
        for s in range(0, len(order), batch_size):
            idx = order[s:s + batch_size]
            enc = tokenizer([jobs[j][1] for j in idx], [jobs[j][2] for j in idx], padding=True,
                            truncation="only_first", max_length=NLI_MAX_LENGTH, return_tensors="pt")
            if device is not None and hasattr(enc, "to"):
                enc = enc.to(device)
            probs = model(**enc).logits.float().softmax(-1)[:, entail_idx].tolist()
            for j, p in zip(idx, probs):
                pair = jobs[j][0]
                scores[pair] = max(scores[pair], float(p))
    stats = {"pairs": len(pairs), "windows": len(jobs), "batches": (len(jobs) + batch_size - 1) // batch_size,
             "total_ms": round((time.perf_counter() - t0) * 1000, 1)}
    return scores, stats
