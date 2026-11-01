#!/usr/bin/env python3
"""
ArxivLens — Milestone 0: Walking Skeleton
Demonstrates end-to-end pipeline locally without external services:
1. Download arXiv PDF & metadata
2. Section-aware text & typography parsing (PyMuPDF)
3. Token-based chunking with BGE tokenizer & section prefixing
4. Vector embedding with BAAI/bge-base-en-v1.5 (MPS/CPU)
5. In-memory vector similarity retrieval
6. Citation-aware RAG answering & support score verification
"""

import os
import re
import sys
import hashlib
import argparse
from typing import List, Dict, Any, Tuple, Optional
import xml.etree.ElementTree as ET

import pymupdf
import numpy as np
import requests
import torch
from transformers import AutoTokenizer, AutoModel


# Configuration defaults matching spec §7 & §9
TARGET_TOKENS = 512
OVERLAP_TOKENS = 75
MIN_TOKENS = 50
MODEL_NAME = "BAAI/bge-base-en-v1.5"
SKIP_SECTIONS = {"references", "bibliography", "acknowledgments", "appendix"}
SUPPORT_THRESHOLD = 0.5


class ArxivDownloader:
    """Fetches paper metadata and PDF from arXiv API."""

    def __init__(self, cache_dir: str = ".cache"):
        self.cache_dir = cache_dir
        os.makedirs(cache_dir, exist_ok=True)

    def get_paper(self, arxiv_id: str) -> Dict[str, Any]:
        """Fetch metadata and download PDF."""
        # Clean arxiv ID
        clean_id = arxiv_id.strip()
        meta_url = f"http://export.arxiv.org/api/query?id_list={clean_id}"
        resp = requests.get(meta_url, timeout=10)
        resp.raise_for_status()

        # Parse Atom XML
        root = ET.fromstring(resp.text)
        ns = {"atom": "http://www.w3.org/2005/Atom"}
        entry = root.find("atom:entry", ns)
        if entry is None:
            raise ValueError(f"No arXiv paper found for ID: {clean_id}")

        title_elem = entry.find("atom:title", ns)
        summary_elem = entry.find("atom:summary", ns)
        published_elem = entry.find("atom:published", ns)

        title = " ".join(title_elem.text.split()) if title_elem is not None and title_elem.text else "Untitled"
        abstract = " ".join(summary_elem.text.split()) if summary_elem is not None and summary_elem.text else ""
        published_at = published_elem.text if published_elem is not None else ""

        authors = [a.find("atom:name", ns).text for a in entry.findall("atom:author", ns) if a.find("atom:name", ns) is not None]
        categories = [c.attrib.get("term") for c in entry.findall("atom:category", ns) if "term" in c.attrib]

        # Download PDF
        pdf_path = os.path.join(self.cache_dir, f"{clean_id}.pdf")
        if not os.path.exists(pdf_path):
            pdf_url = f"https://arxiv.org/pdf/{clean_id}.pdf"
            headers = {"User-Agent": "ArxivLens/1.0 (Research Pipeline; mailto:contact@arxivlens.local)"}
            pdf_resp = requests.get(pdf_url, headers=headers, timeout=30)
            pdf_resp.raise_for_status()
            with open(pdf_path, "wb") as f:
                f.write(pdf_resp.content)

        with open(pdf_path, "rb") as f:
            sha256 = hashlib.sha256(f.read()).hexdigest()

        return {
            "arxiv_id": clean_id,
            "title": title,
            "abstract": abstract,
            "authors": authors,
            "categories": categories,
            "published_at": published_at,
            "pdf_path": pdf_path,
            "sha256": sha256,
        }


class PaperParser:
    """Extracts typography and structured sections from PDF using PyMuPDF."""

    def parse_pdf(self, pdf_path: str, paper_title: str) -> List[Dict[str, Any]]:
        """
        Parses document into ordered sections with heading hierarchy inferred from font size.
        Returns: list of dicts with keys: {'title': str, 'level': int, 'text': str, 'page': int}
        """
        doc = pymupdf.open(pdf_path)
        sections: List[Dict[str, Any]] = []
        current_section = {
            "title": "Abstract",
            "level": 1,
            "text": "",
            "page": 1,
        }

        # Calculate average font size to determine heading thresholds
        font_sizes = []
        for page in doc:
            blocks = page.get_text("dict", flags=11)["blocks"]
            for b in blocks:
                if b.get("type") == 0:  # text block
                    for line in b.get("lines", []):
                        for span in line.get("spans", []):
                            if span.get("text", "").strip():
                                font_sizes.append(span.get("size", 10.0))

        base_font_size = float(np.median(font_sizes)) if font_sizes else 10.0
        heading_threshold = base_font_size * 1.15  # Headings are typically 15%+ larger

        for page_idx, page in enumerate(doc):
            blocks = page.get_text("dict", flags=11)["blocks"]
            for b in blocks:
                if b.get("type") != 0:
                    continue

                for line in b.get("lines", []):
                    line_text = " ".join([span.get("text", "") for span in line.get("spans", []) if span.get("text", "").strip()]).strip()
                    if not line_text:
                        continue

                    # Check if line looks like a heading
                    max_span_size = max([span.get("size", 0.0) for span in line.get("spans", [])], default=0.0)
                    is_bold = any("bold" in span.get("font", "").lower() for span in line.get("spans", []))
                    is_numbered_heading = bool(re.match(r"^(\d+\.?\d*|[IVXLCDM]+\.)\s+[A-Z]", line_text))

                    is_heading = (max_span_size >= heading_threshold or is_numbered_heading or (is_bold and len(line_text) < 60)) and len(line_text) < 100

                    if is_heading:
                        # Clean heading text
                        norm_title = re.sub(r"^(\d+\.?\d*|[IVXLCDM]+\.)\s*", "", line_text).strip()
                        if current_section["text"].strip():
                            sections.append(current_section)
                        current_section = {
                            "title": norm_title or line_text,
                            "level": 1 if max_span_size >= base_font_size * 1.3 else 2,
                            "text": "",
                            "page": page_idx + 1,
                        }
                    else:
                        current_section["text"] += line_text + " "

        if current_section["text"].strip():
            sections.append(current_section)

        doc.close()
        return sections


class TokenChunker:
    """Token-based chunker with BGE tokenizer, section prefixing, and junk filtering."""

    def __init__(
        self,
        model_name: str = MODEL_NAME,
        target_tokens: int = TARGET_TOKENS,
        overlap_tokens: int = OVERLAP_TOKENS,
        min_tokens: int = MIN_TOKENS,
        skip_sections: set = None,
    ):
        self.tokenizer = AutoTokenizer.from_pretrained(model_name)
        self.tokenizer.model_max_length = int(1e9)
        self.target_tokens = target_tokens
        self.overlap_tokens = overlap_tokens
        self.min_tokens = min_tokens
        self.skip_sections = skip_sections or SKIP_SECTIONS

    def chunk_sections(self, paper_title: str, sections: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
        """
        Splits sections into token-bounded chunks with section prefixing.
        Never allows chunks to span across top-level sections.
        """
        all_chunks: List[Dict[str, Any]] = []
        chunk_idx = 0

        for sec in sections:
            sec_title = sec.get("title", "").strip()
            # Drop junk sections like references and bibliographies
            if sec_title.lower() in self.skip_sections:
                continue

            sec_text = sec.get("text", "").strip()
            if not sec_text:
                continue

            # Tokenize section text
            tokens = self.tokenizer.encode(sec_text, add_special_tokens=False)
            if len(tokens) < self.min_tokens:
                continue

            prefix = f"{paper_title} — {sec_title}\n\n"
            prefix_tokens = self.tokenizer.encode(prefix, add_special_tokens=False)
            effective_target = max(self.min_tokens, self.target_tokens - len(prefix_tokens) - 16)
            effective_overlap = min(self.overlap_tokens, effective_target // 2)

            step = max(1, effective_target - effective_overlap)
            for start_i in range(0, len(tokens), step):
                chunk_token_slice = tokens[start_i : start_i + effective_target]
                if len(chunk_token_slice) < self.min_tokens and len(all_chunks) > 0 and start_i > 0:
                    continue


                raw_chunk_text = self.tokenizer.decode(chunk_token_slice, skip_special_tokens=True)
                full_content = prefix + raw_chunk_text
                content_hash = hashlib.sha256(full_content.strip().encode("utf-8")).hexdigest()

                all_chunks.append({
                    "chunk_index": chunk_idx,
                    "section_title": sec_title,
                    "page_number": sec.get("page", 1),
                    "token_count": len(chunk_token_slice) + len(prefix_tokens),
                    "content": full_content,
                    "content_hash": content_hash,
                })
                chunk_idx += 1

                if start_i + effective_target >= len(tokens):
                    break

        return all_chunks


class BGEEmbedder:
    """Generates normalized vector embeddings using BAAI/bge-base-en-v1.5."""

    def __init__(self, model_name: str = MODEL_NAME):
        self.device = "mps" if torch.backends.mps.is_available() else ("cuda" if torch.cuda.is_available() else "cpu")
        self.tokenizer = AutoTokenizer.from_pretrained(model_name)
        self.tokenizer.model_max_length = 512
        self.model = AutoModel.from_pretrained(model_name).to(self.device)
        self.model.eval()

    def embed_texts(self, texts: List[str], is_query: bool = False) -> np.ndarray:
        """
        Embeds a list of texts. For BGE queries, prepends instruction if needed.
        Returns normalized vectors of shape (len(texts), 768).
        """
        if is_query:
            # BGE recommended query instruction
            texts = [f"Represent this sentence for searching relevant passages: {t}" for t in texts]

        encoded = self.tokenizer(texts, padding=True, truncation=True, max_length=512, return_tensors="pt").to(self.device)
        with torch.no_grad():
            outputs = self.model(**encoded)
            # CLS pooling
            embeddings = outputs[0][:, 0]
            # Normalize embeddings to unit sphere for cosine distance
            embeddings = torch.nn.functional.normalize(embeddings, p=2, dim=1)

        return embeddings.cpu().numpy()


class CitationValidator:
    """Validates citations in generated responses and checks sentence-level support scores."""

    def __init__(self, embedder: BGEEmbedder, threshold: float = SUPPORT_THRESHOLD):
        self.embedder = embedder
        self.threshold = threshold

    def validate_and_verify(
        self,
        answer: str,
        retrieved_chunks: List[Dict[str, Any]],
    ) -> Dict[str, Any]:
        """
        Parses [n] citations from the answer, validates marker boundaries,
        and computes cosine similarity between cited sentences and the corresponding chunk.
        """
        k = len(retrieved_chunks)
        # Regex to split into sentences while preserving markers
        sentence_splits = re.split(r"(?<=[.!?])\s+", answer.strip())
        
        citations_found = []
        invalid_markers = []
        verified_citations = []

        for sentence in sentence_splits:
            markers = [int(m) for m in re.findall(r"\[(\d+)\]", sentence)]
            for marker in markers:
                citations_found.append(marker)
                if marker < 1 or marker > k:
                    invalid_markers.append(marker)
                    continue

                chunk = retrieved_chunks[marker - 1]
                # Verify support: embed sentence and chunk, compute cosine similarity
                sent_clean = re.sub(r"\[\d+\]", "", sentence).strip()
                if not sent_clean:
                    continue

                sent_emb = self.embedder.embed_texts([sent_clean], is_query=False)[0]
                chunk_emb = chunk["embedding"]
                cos_sim = float(np.dot(sent_emb, chunk_emb))

                is_supported = cos_sim >= self.threshold
                verified_citations.append({
                    "marker": marker,
                    "arxiv_id": chunk.get("arxiv_id", "unknown"),
                    "title": chunk.get("paper_title", "unknown"),
                    "chunk_id": chunk.get("chunk_index", 0),
                    "section": chunk.get("section_title", ""),
                    "score": round(cos_sim, 4),
                    "supported": is_supported,
                    "sentence": sentence,
                })

        return {
            "answer": answer,
            "citations_total": len(citations_found),
            "invalid_markers": invalid_markers,
            "citations": verified_citations,
        }


def run_pipeline(arxiv_id: str, question: str) -> None:
    print(f"\n=======================================================")
    print(f"🚀 ArxivLens Walking Skeleton — Processing {arxiv_id}")
    print(f"=======================================================\n")

    downloader = ArxivDownloader()
    print(f"[1/5] Fetching arXiv metadata & downloading PDF...")
    meta = downloader.get_paper(arxiv_id)
    print(f"  ✓ Title: {meta['title']}")
    print(f"  ✓ Authors: {', '.join(meta['authors'][:3])}")
    print(f"  ✓ SHA-256: {meta['sha256'][:16]}...")

    parser = PaperParser()
    print(f"\n[2/5] Parsing PDF typography & structure with PyMuPDF...")
    sections = parser.parse_pdf(meta["pdf_path"], meta["title"])
    print(f"  ✓ Extracted {len(sections)} sections:")
    for s in sections[:5]:
        print(f"    - [{s['title']}] (Page {s['page']}, Level {s['level']}, Length: {len(s['text'])} chars)")

    chunker = TokenChunker()
    print(f"\n[3/5] Chunking sections with BGE tokenizer & section prefixing...")
    chunks = chunker.chunk_sections(meta["title"], sections)
    print(f"  ✓ Produced {len(chunks)} chunks (Target: 512 tokens, Overlap: 75 tokens)")
    print(f"  Sample Chunk #0 Prefix & Header:\n    {repr(chunks[0]['content'][:120])}...")

    embedder = BGEEmbedder()
    print(f"\n[4/5] Embedding chunks with {MODEL_NAME} on {embedder.device}...")
    chunk_texts = [c["content"] for c in chunks]
    vectors = embedder.embed_texts(chunk_texts)
    for i, c in enumerate(chunks):
        c["embedding"] = vectors[i]
        c["arxiv_id"] = meta["arxiv_id"]
        c["paper_title"] = meta["title"]
    print(f"  ✓ Successfully computed {vectors.shape[0]} embeddings of dimension {vectors.shape[1]}")

    print(f"\n[5/5] Performing Vector Retrieval for Question:\n  \"{question}\"")
    q_vec = embedder.embed_texts([question], is_query=True)[0]
    
    # Compute cosine similarity
    similarities = [float(np.dot(q_vec, c["embedding"])) for c in chunks]
    top_indices = np.argsort(similarities)[::-1][:3]
    top_chunks = [chunks[i] for i in top_indices]

    print(f"\n  Top {len(top_chunks)} Retrieved Contexts:")
    for rank, idx in enumerate(top_indices, 1):
        c = chunks[idx]
        print(f"  [{rank}] Score: {similarities[idx]:.4f} | Section: {c['section_title']} (Page {c['page_number']})")
        print(f"      Content snippet: {c['content'][:140].replace(chr(10), ' ')}...")

    # Generate sample RAG response using top context
    top_sec = top_chunks[0]["section_title"]
    simulated_answer = (
        f"The paper addresses this in the {top_sec} section by proposing optimized representations [1]. "
        f"Additionally, the method evaluates performance improvements across benchmarks [2]."
    )

    validator = CitationValidator(embedder)
    print(f"\n[RAG Citation Verification]")
    print(f"  Simulated Answer: \"{simulated_answer}\"")
    val_result = validator.validate_and_verify(simulated_answer, top_chunks)

    print(f"\n  Citations Extracted: {val_result['citations_total']}")
    print(f"  Invalid Markers (outside 1..K): {val_result['invalid_markers']}")
    print(f"  Groundedness / Support Verification:")
    for cit in val_result["citations"]:
        status = "✅ SUPPORTED" if cit["supported"] else "❌ UNSUPPORTED"
        print(f"    - Marker [{cit['marker']}] -> {cit['section']} | Sim: {cit['score']:.4f} -> {status}")

    print(f"\n✨ Walking Skeleton execution complete!\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="ArxivLens Walking Skeleton")
    parser.add_argument("--arxiv-id", default="2401.12345", help="arXiv paper ID to process")
    parser.add_argument("--question", default="What method does the paper propose and what are its key contributions?", help="Question to ask")
    args = parser.parse_args()

    run_pipeline(args.arxiv_id, args.question)
