"""Embedding service for Code Atlas.

Turns repository text and search queries into dense vectors for Elasticsearch
kNN / hybrid search. The model runs on CPU through ONNX Runtime (fastembed),
so the service needs no GPU and no PyTorch.
"""

from __future__ import annotations

import os
import time
from functools import lru_cache
from typing import Protocol, Sequence

from fastapi import Depends, FastAPI, HTTPException
from pydantic import BaseModel, Field

MODEL_NAME = "sentence-transformers/all-MiniLM-L6-v2"
DIM = 384
MAX_TEXTS = 256
MAX_CHARS = 4_000  # the model truncates at 256 word pieces; longer input is wasted work


class Embedder(Protocol):
    def embed(self, texts: Sequence[str]) -> list[list[float]]: ...


class FastEmbedder:
    def __init__(self, model_dir: str):
        from fastembed import TextEmbedding  # imported lazily so tests run without the model

        self._model = TextEmbedding(MODEL_NAME, specific_model_path=model_dir)

    def embed(self, texts: Sequence[str]) -> list[list[float]]:
        # all-MiniLM-L6-v2 output is L2-normalized, so dot product == cosine similarity.
        return [vector.tolist() for vector in self._model.embed(list(texts))]


@lru_cache(maxsize=1)
def get_embedder() -> Embedder:
    return FastEmbedder(os.environ.get("MODEL_DIR", "/models/all-MiniLM-L6-v2"))


class EmbedRequest(BaseModel):
    texts: list[str] = Field(min_length=1, max_length=MAX_TEXTS)


class EmbedResponse(BaseModel):
    model: str
    dim: int
    vectors: list[list[float]]
    took_ms: float


app = FastAPI(title="Code Atlas AI worker")


@app.get("/health")
def health(embedder: Embedder = Depends(get_embedder)) -> dict:
    return {"status": "ok", "model": MODEL_NAME, "dim": DIM}


@app.post("/embed", response_model=EmbedResponse)
def embed(req: EmbedRequest, embedder: Embedder = Depends(get_embedder)) -> EmbedResponse:
    texts = [t.strip()[:MAX_CHARS] for t in req.texts]
    if any(not t for t in texts):
        raise HTTPException(status_code=422, detail="texts must not be empty")
    start = time.perf_counter()
    vectors = embedder.embed(texts)
    if len(vectors) != len(texts) or any(len(v) != DIM for v in vectors):
        raise HTTPException(status_code=500, detail="embedder returned unexpected output")
    return EmbedResponse(
        model=MODEL_NAME, dim=DIM, vectors=vectors, took_ms=round((time.perf_counter() - start) * 1000, 2)
    )
