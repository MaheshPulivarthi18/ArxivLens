"""ADR-0020: the reranker runs fp16 on MPS/CUDA and fp32 on CPU; EMBEDDER_RERANK_FP16=0 forces fp32. No torch needed."""
import os

import pytest

from services.embedder.precision import rerank_fp16

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


@pytest.mark.parametrize("device,want", [("mps", True), ("cuda", True), ("cuda:1", True), ("cpu", False), ("", False)])
def test_fp16_on_gpu_devices_only(device, want):
    assert rerank_fp16(device, env={}) is want


@pytest.mark.parametrize("value", ["0", "false", "No", " off "])
def test_env_switch_forces_fp32(value):
    assert rerank_fp16("mps", env={"EMBEDDER_RERANK_FP16": value}) is False


@pytest.mark.parametrize("value", ["", "1", "true"])
def test_env_other_values_keep_default(value):
    assert rerank_fp16("cuda", env={"EMBEDDER_RERANK_FP16": value}) is True


def test_api_halves_the_reranker_only_when_chosen():
    src = open(os.path.join(ROOT, "services", "embedder", "api.py"), encoding="utf-8").read()
    assert "if rerank_fp16(embedder.device):\n            reranker_model.half()" in src
    # logits are cast to fp32 before the sigmoid, so scores stay fp32 floats
    assert "logits.view(-1, ).float()" in src
    assert '"reranker_dtype"' in src
