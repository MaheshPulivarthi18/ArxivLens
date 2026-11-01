import requests
import pytest


BASE_URL = "http://localhost:8080"


def test_query_service_health():
    resp = requests.get(f"{BASE_URL}/healthz", timeout=5)
    assert resp.status_code == 200
    assert resp.json().get("status") == "ok"


def test_hybrid_search_with_rrf_and_rerank():
    payload = {
        "query": "medical report reward learning checklist",
        "limit": 5,
        "rerank": True,
    }
    resp = requests.post(f"{BASE_URL}/v1/search", json=payload, timeout=10)
    assert resp.status_code == 200
    data = resp.json()
    assert data["count"] > 0
    assert len(data["hits"]) > 0

    top_hit = data["hits"][0]
    assert "arxiv_id" in top_hit
    assert "title" in top_hit
    assert "content" in top_hit
    assert top_hit["rrf_score"] > 0
    assert top_hit["rerank_score"] > 0.50


def test_rag_ask_with_groundedness_support():
    payload = {
        "question": "How does G-CARL improve patient-oriented medical report interpretation?",
        "max_citations": 3,
    }
    resp = requests.post(f"{BASE_URL}/v1/ask", json=payload, timeout=10)
    assert resp.status_code == 200
    data = resp.json()

    assert "answer" in data
    assert "citations" in data
    assert len(data["citations"]) >= 1
    assert len(data["trace_id"]) == 32

    # Verify citation structure matching §9
    for cit in data["citations"]:
        assert cit["marker"] in (1, 2, 3)
        assert cit["score"] > 0.50
        assert cit["supported"] is True
        assert cit["title"] != ""


def test_rag_early_refusal_on_unrelated_query():
    payload = {
        "question": "How to bake a chocolate sourdough cake with sourdough starter?",
    }
    resp = requests.post(f"{BASE_URL}/v1/ask", json=payload, timeout=10)
    assert resp.status_code == 200
    data = resp.json()

    assert data["answer"] == "No indexed papers are relevant to this question."
    assert data["citations"] == []
    assert len(data["trace_id"]) == 32


def test_paper_status_endpoint():
    resp = requests.get(f"{BASE_URL}/v1/papers/2608.20331/status", timeout=5)
    assert resp.status_code == 200
    data = resp.json()
    assert data["ArxivID"] == "2608.20331"
    assert data["Status"] == "embedded"
    assert len(data["SHA256"]) == 64
