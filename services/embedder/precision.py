"""Reranker precision choice (ADR-0020). No torch import, so the light test suite can check it."""
import os
from typing import Mapping, Optional

# Devices where fp16 is fast. On CPU, fp16 matmuls are slower than fp32, so the reranker stays fp32 there.
FP16_DEVICES = ("mps", "cuda")


def rerank_fp16(device: str, env: Optional[Mapping[str, str]] = None) -> bool:
    """True when the cross-encoder reranker should run in fp16: a GPU device (MPS or CUDA) and
    EMBEDDER_RERANK_FP16 not set to 0/false/no/off. Measured on MPS: same eval ranking, ~25% faster."""
    env = os.environ if env is None else env
    if env.get("EMBEDDER_RERANK_FP16", "").strip().lower() in ("0", "false", "no", "off"):
        return False
    return str(device).split(":", 1)[0] in FP16_DEVICES
