"""Tests for the whisper sidecar's request body cap.

The real WhisperModel is stubbed out before main is imported: loading
large-v3 costs seconds and gigabytes, and none of the body-cap
behaviour under test touches transcription.
"""

import sys
import types

import pytest

_stub = types.ModuleType("faster_whisper")


class _FakeModel:
    def __init__(self, *args, **kwargs):
        pass

    def transcribe(self, *args, **kwargs):
        segment = types.SimpleNamespace(text="hello there")
        return [segment], None


_stub.WhisperModel = _FakeModel
sys.modules.setdefault("faster_whisper", _stub)

import main  # noqa: E402
from fastapi.testclient import TestClient  # noqa: E402
from main import MAX_BODY_BYTES, app  # noqa: E402

client = TestClient(app)


def test_health():
    assert client.get("/health").status_code == 200


def test_oversized_declared_body_rejected():
    resp = client.post("/transcribe", content=b"x" * (MAX_BODY_BYTES + 1))
    assert resp.status_code == 413
    assert resp.json()["detail"] == "request body too large"


def test_oversized_chunked_body_rejected():
    def chunks():
        for _ in range((MAX_BODY_BYTES // (1 << 20)) + 2):
            yield b"x" * (1 << 20)

    resp = client.post("/transcribe", content=chunks())
    assert resp.status_code == 413


def test_invalid_content_length_rejected():
    resp = client.post("/transcribe", content=b"hi", headers={"content-length": "abc"})
    assert resp.status_code == 400


def test_oversized_body_not_transcribed(monkeypatch):
    """The cap must fire before the model decodes anything."""
    called = False

    def fail(*args, **kwargs):
        nonlocal called
        called = True
        raise AssertionError("model must not run for an oversized body")

    monkeypatch.setattr(main.model, "transcribe", fail)
    resp = client.post("/transcribe", content=b"x" * (MAX_BODY_BYTES + 1))
    assert resp.status_code == 413
    assert called is False


def test_empty_body_rejected():
    assert client.post("/transcribe", content=b"").status_code == 400


def test_within_cap_body_transcribes():
    resp = client.post("/transcribe", content=b"fake audio bytes")
    assert resp.status_code == 200
    assert resp.json()["text"] == "hello there"
