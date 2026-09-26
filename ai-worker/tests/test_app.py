import math
import os

import pytest
from fastapi.testclient import TestClient

import app as worker


class FakeEmbedder:
    def __init__(self):
        self.calls = []

    def embed(self, texts):
        self.calls.append(list(texts))
        return [[float(len(t))] + [0.0] * (worker.DIM - 1) for t in texts]


@pytest.fixture
def fake():
    fake = FakeEmbedder()
    worker.app.dependency_overrides[worker.get_embedder] = lambda: fake
    yield fake
    worker.app.dependency_overrides.clear()


def test_embed_returns_one_vector_per_text(fake):
    res = TestClient(worker.app).post("/embed", json={"texts": ["kafka", "vector database"]})
    assert res.status_code == 200
    body = res.json()
    assert body["dim"] == worker.DIM and body["model"] == worker.MODEL_NAME
    assert [v[0] for v in body["vectors"]] == [5.0, 15.0]


def test_embed_trims_and_truncates_input(fake):
    TestClient(worker.app).post("/embed", json={"texts": ["  x  ", "y" * 10_000]})
    assert fake.calls[0][0] == "x"
    assert len(fake.calls[0][1]) == worker.MAX_CHARS


@pytest.mark.parametrize(
    "payload",
    [{"texts": []}, {"texts": ["  "]}, {"texts": ["a"] * (worker.MAX_TEXTS + 1)}, {}],
)
def test_embed_rejects_bad_input(fake, payload):
    assert TestClient(worker.app).post("/embed", json=payload).status_code == 422


def test_health(fake):
    assert TestClient(worker.app).get("/health").json()["dim"] == worker.DIM


@pytest.mark.skipif(not os.path.exists(os.path.join(os.environ.get("MODEL_DIR", "/nonexistent"), "model.onnx")),
                    reason="set MODEL_DIR to a downloaded model to run")
def test_real_model_similarity():
    embedder = worker.FastEmbedder(os.environ["MODEL_DIR"])
    q, kafka, css = embedder.embed(["messaging system", "Apache Kafka event streaming", "utility-first CSS framework"])
    assert len(q) == worker.DIM
    assert math.isclose(sum(x * x for x in q), 1.0, rel_tol=1e-3)
    dot = lambda a, b: sum(x * y for x, y in zip(a, b))
    assert dot(q, kafka) > dot(q, css)
