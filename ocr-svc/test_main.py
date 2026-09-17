"""Tests for the ocr sidecar's request body cap.

pytesseract is stubbed out before main is imported: the real module
shells out to the tesseract CLI, and none of the body-cap behaviour
under test touches recognition.
"""

import io
import sys
import types

_stub = types.ModuleType("pytesseract")
_stub.image_to_string = lambda *args, **kwargs: "hello there"
sys.modules.setdefault("pytesseract", _stub)

import main  # noqa: E402
from fastapi.testclient import TestClient  # noqa: E402
from main import MAX_BODY_BYTES, app  # noqa: E402
from PIL import Image  # noqa: E402

client = TestClient(app)


def png_bytes() -> bytes:
    """A real 1x1 PNG, so Pillow's own decode succeeds."""
    buf = io.BytesIO()
    Image.new("RGB", (1, 1)).save(buf, format="PNG")
    return buf.getvalue()


def test_healthz():
    assert client.get("/healthz").status_code == 200


def test_oversized_declared_body_rejected():
    resp = client.post("/recognize", content=b"x" * (MAX_BODY_BYTES + 1))
    assert resp.status_code == 413
    assert resp.json()["detail"] == "request body too large"


def test_oversized_chunked_body_rejected():
    def chunks():
        for _ in range((MAX_BODY_BYTES // (1 << 20)) + 2):
            yield b"x" * (1 << 20)

    resp = client.post("/recognize", content=chunks())
    assert resp.status_code == 413


def test_invalid_content_length_rejected():
    resp = client.post("/recognize", content=b"hi", headers={"content-length": "abc"})
    assert resp.status_code == 400


def test_oversized_body_not_recognized(monkeypatch):
    """The cap must fire before Pillow/tesseract decode anything."""
    called = False

    def fail(*args, **kwargs):
        nonlocal called
        called = True
        raise AssertionError("ocr must not run for an oversized body")

    monkeypatch.setattr(main.pytesseract, "image_to_string", fail)
    resp = client.post("/recognize", content=b"x" * (MAX_BODY_BYTES + 1))
    assert resp.status_code == 413
    assert called is False


def test_empty_body_rejected():
    assert client.post("/recognize", content=b"").status_code == 400


def test_within_cap_body_recognizes():
    resp = client.post("/recognize", content=png_bytes())
    assert resp.status_code == 200
    assert resp.json()["text"] == "hello there"
