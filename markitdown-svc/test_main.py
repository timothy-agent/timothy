"""Tests for the markitdown sidecar's request body cap."""

import main
from fastapi.testclient import TestClient
from main import MAX_BODY_BYTES, app

client = TestClient(app)


def test_healthz():
    assert client.get("/healthz").status_code == 200


def test_oversized_declared_body_rejected():
    resp = client.post("/convert", content=b"x" * (MAX_BODY_BYTES + 1))
    assert resp.status_code == 413
    assert resp.json()["detail"] == "request body too large"


def test_oversized_chunked_body_rejected():
    def chunks():
        for _ in range((MAX_BODY_BYTES // (1 << 20)) + 2):
            yield b"x" * (1 << 20)

    resp = client.post("/convert", content=chunks())
    assert resp.status_code == 413


def test_invalid_content_length_rejected():
    resp = client.post("/convert", content=b"hi", headers={"content-length": "abc"})
    assert resp.status_code == 400


def test_oversized_body_not_converted(monkeypatch):
    """The cap must fire before markitdown ever sees the bytes."""
    called = False

    def fail(*args, **kwargs):
        nonlocal called
        called = True
        raise AssertionError("converter must not run for an oversized body")

    monkeypatch.setattr(main.converter, "convert_stream", fail)
    resp = client.post("/convert", content=b"x" * (MAX_BODY_BYTES + 1))
    assert resp.status_code == 413
    assert called is False


def test_oversized_pdf_images_body_rejected():
    resp = client.post("/pdf/images", content=b"x" * (MAX_BODY_BYTES + 1))
    assert resp.status_code == 413


def test_empty_body_rejected():
    assert client.post("/convert", content=b"").status_code == 400


def test_within_cap_body_converts():
    resp = client.post(
        "/convert",
        content=b"# Title\n\nbody text\n",
        headers={"x-filename": "note.md", "x-mimetype": "text/markdown"},
    )
    assert resp.status_code == 200
    assert "body text" in resp.json()["markdown"]
