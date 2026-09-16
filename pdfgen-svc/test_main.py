"""Tests for the pdfgen sidecar's request bounds: body cap, typst
compile timeout, and temp-path scrubbing in returned diagnostics."""

import subprocess

import pytest
from fastapi.testclient import TestClient

import main
from main import MAX_BODY_BYTES, app

client = TestClient(app)


def _render_body(content: str = "hello") -> dict:
    return {"documents": [{"title": "Doc", "content": content}], "options": {}}


def test_healthz():
    assert client.get("/healthz").status_code == 200


def test_oversized_declared_body_rejected():
    oversized = "x" * (MAX_BODY_BYTES + 1)
    resp = client.post("/render", content=oversized, headers={"content-type": "application/json"})
    assert resp.status_code == 413
    assert resp.json()["error"] == "request body too large"


def test_oversized_chunked_body_rejected():
    def chunks():
        for _ in range((MAX_BODY_BYTES // (1 << 20)) + 2):
            yield b"x" * (1 << 20)

    resp = client.post("/render", content=chunks(), headers={"content-type": "application/json"})
    assert resp.status_code == 413


def test_invalid_content_length_rejected():
    resp = client.post(
        "/render",
        content=b"{}",
        headers={"content-type": "application/json", "content-length": "not-a-number"},
    )
    assert resp.status_code == 400


def test_oversized_body_not_parsed(monkeypatch):
    """The cap must fire before the route runs, not after buffering."""
    called = False

    def fail(*args, **kwargs):
        nonlocal called
        called = True
        raise AssertionError("compile must not run for an oversized body")

    monkeypatch.setattr(main, "_compile", fail)
    resp = client.post(
        "/render",
        content="x" * (MAX_BODY_BYTES + 1),
        headers={"content-type": "application/json"},
    )
    assert resp.status_code == 413
    assert called is False


def test_compile_timeout_returns_504(monkeypatch):
    def slow(*args, **kwargs):
        raise subprocess.TimeoutExpired(cmd=["typst"], timeout=main.TYPST_TIMEOUT_SECONDS)

    monkeypatch.setattr(main, "_compile", slow)
    resp = client.post("/render", json=_render_body())
    assert resp.status_code == 504
    assert "timed out" in resp.json()["error"]


def test_compile_timeout_leaves_service_responsive(monkeypatch):
    def slow(*args, **kwargs):
        raise subprocess.TimeoutExpired(cmd=["typst"], timeout=main.TYPST_TIMEOUT_SECONDS)

    monkeypatch.setattr(main, "_compile", slow)
    assert client.post("/render", json=_render_body()).status_code == 504
    assert client.get("/healthz").status_code == 200


def test_compile_actually_times_out():
    """The real subprocess call honours the timeout and reaps the child."""
    with pytest.raises(subprocess.TimeoutExpired):
        subprocess.run(["sleep", "5"], capture_output=True, text=True, timeout=0.2)


@pytest.mark.parametrize(
    ("raw", "want"),
    [
        (
            "error: file not found\n  ┌─ /tmp/tmpab12cd/main.typ:4:2",
            "error: file not found\n  ┌─ main.typ:4:2",
        ),
        (
            "error: unexpected token in /tmp/tmpxyz/doc3.md line 7",
            "error: unexpected token in doc3.md line 7",
        ),
        (
            "error: cannot write /private/var/folders/qq/T/tmp9/out.pdf",
            "error: cannot write out.pdf",
        ),
        ("error: unknown variable: foo", "error: unknown variable: foo"),
    ],
)
def test_scrub_stderr(raw, want):
    assert main._scrub_stderr(raw) == want


def test_compile_failure_response_has_no_temp_path(monkeypatch):
    def failing(workdir, out_pdf):
        return subprocess.CompletedProcess(
            args=["typst"],
            returncode=1,
            stdout="",
            stderr=f"error: syntax error\n  ┌─ {workdir}/main.typ:2:1",
        )

    monkeypatch.setattr(main, "_compile", failing)
    resp = client.post("/render", json=_render_body())
    assert resp.status_code == 500
    error = resp.json()["error"]
    assert "main.typ:2:1" in error
    assert "/tmp" not in error
    assert "/var/folders" not in error


def test_normal_render_succeeds(monkeypatch):
    """A within-cap request still reaches the compile path unchanged."""
    seen = {}

    def ok(workdir, out_pdf):
        seen["workdir"] = workdir
        out_pdf.write_bytes(b"%PDF-1.7 fake")
        return subprocess.CompletedProcess(args=["typst"], returncode=0, stdout="", stderr="")

    monkeypatch.setattr(main, "_compile", ok)
    resp = client.post("/render", json=_render_body())
    assert resp.status_code == 200
    assert resp.content == b"%PDF-1.7 fake"
    assert (seen["workdir"] / "main.typ").exists() is False  # tempdir cleaned up
