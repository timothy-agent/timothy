"""Tests for the pdfgen sidecar's request bounds: body cap, typst
compile timeout, and temp-path scrubbing in returned diagnostics."""

import shutil
import struct
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


# /rasterize: SVG in, PNG out. Tests marked needs_typst run the real
# binary and only run inside the pdfgen image.

SVG_TEXT = (
    b'<svg xmlns="http://www.w3.org/2000/svg" width="3200" height="800">'
    b'<rect width="3200" height="800" fill="#eef"/>'
    b'<text x="40" y="400" font-size="200">Hello Typst</text></svg>'
)
SVG_VIEWBOX_ONLY = (
    b'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 100">'
    b'<rect width="400" height="100" fill="green"/></svg>'
)

needs_typst = pytest.mark.skipif(shutil.which("typst") is None, reason="typst binary not installed")


def _png_size(data: bytes) -> tuple[int, int]:
    assert data[:8] == b"\x89PNG\r\n\x1a\n"
    return struct.unpack(">II", data[16:24])


@needs_typst
def test_rasterize_caps_longest_side():
    resp = client.post("/rasterize", content=SVG_TEXT, headers={"content-type": "image/svg+xml"})
    assert resp.status_code == 200, resp.text
    assert resp.headers["content-type"] == "image/png"
    assert _png_size(resp.content) == (1600, 400)


@needs_typst
def test_rasterize_viewbox_only_keeps_aspect():
    resp = client.post("/rasterize", content=SVG_VIEWBOX_ONLY)
    assert resp.status_code == 200, resp.text
    width, height = _png_size(resp.content)
    assert max(width, height) <= 1600
    assert width == 4 * height


@needs_typst
def test_rasterize_small_icon_grows_to_floor():
    icon = b'<svg xmlns="http://www.w3.org/2000/svg" width="16" height="8"><rect width="16" height="8"/></svg>'
    resp = client.post("/rasterize", content=icon)
    assert resp.status_code == 200, resp.text
    assert _png_size(resp.content) == (512, 256)


@needs_typst
def test_rasterize_malformed_svg_fails_cleanly():
    resp = client.post("/rasterize", content=b'<svg xmlns="http://www.w3.org/2000/svg" width="10"><rect')
    assert resp.status_code == 422
    error = resp.json()["error"]
    assert "failed to parse SVG" in error
    assert "/tmp" not in error


def test_rasterize_oversized_body_rejected(monkeypatch):
    monkeypatch.setattr(main, "_rasterize", lambda *a: pytest.fail("rasterize must not run"))
    resp = client.post("/rasterize", content=b"x" * (main.MAX_SVG_BYTES + 1))
    assert resp.status_code == 413


def test_rasterize_oversized_chunked_body_rejected(monkeypatch):
    monkeypatch.setattr(main, "_rasterize", lambda *a: pytest.fail("rasterize must not run"))

    def chunks():
        for _ in range((main.MAX_SVG_BYTES // (1 << 20)) + 2):
            yield b"x" * (1 << 20)

    resp = client.post("/rasterize", content=chunks())
    assert resp.status_code == 413


def test_render_cap_unchanged_by_rasterize_cap(monkeypatch):
    """A render body between the two caps is still accepted."""

    def ok(workdir, out_pdf):
        out_pdf.write_bytes(b"%PDF-1.7 fake")
        return subprocess.CompletedProcess(args=["typst"], returncode=0, stdout="", stderr="")

    monkeypatch.setattr(main, "_compile", ok)
    resp = client.post("/render", json=_render_body("x" * (main.MAX_SVG_BYTES + 1)))
    assert resp.status_code == 200


def test_rasterize_empty_body_rejected():
    assert client.post("/rasterize", content=b"  ").status_code == 400


def test_rasterize_timeout_returns_504(monkeypatch):
    def slow(*args, **kwargs):
        raise subprocess.TimeoutExpired(cmd=["typst"], timeout=main.RASTERIZE_TIMEOUT_SECONDS)

    monkeypatch.setattr(main, "_rasterize", slow)
    resp = client.post("/rasterize", content=SVG_TEXT)
    assert resp.status_code == 504
    assert "timed out" in resp.json()["error"]
    assert client.get("/healthz").status_code == 200


def test_rasterize_failure_response_has_no_temp_path(monkeypatch):
    def failing(workdir, out_png):
        return subprocess.CompletedProcess(
            args=["typst"],
            returncode=1,
            stdout="",
            stderr=f"error: failed to parse SVG\n  ┌─ {workdir}/in.svg:1:0",
        )

    monkeypatch.setattr(main, "_rasterize", failing)
    resp = client.post("/rasterize", content=SVG_TEXT)
    assert resp.status_code == 422
    error = resp.json()["error"]
    assert "in.svg:1:0" in error
    assert "/tmp" not in error


def test_rasterize_writes_inputs(monkeypatch):
    seen = {}

    def ok(workdir, out_png):
        seen["svg"] = (workdir / "in.svg").read_bytes()
        seen["typ"] = (workdir / "main.typ").read_text()
        out_png.write_bytes(b"\x89PNG fake")
        return subprocess.CompletedProcess(args=["typst"], returncode=0, stdout="", stderr="")

    monkeypatch.setattr(main, "_rasterize", ok)
    resp = client.post("/rasterize", content=SVG_TEXT)
    assert resp.status_code == 200
    assert resp.content == b"\x89PNG fake"
    assert seen["svg"] == SVG_TEXT
    assert seen["typ"] == main.RASTERIZE_TYP


def test_rasterize_command_is_bounded(monkeypatch, tmp_path):
    seen = {}

    def fake_run(cmd, **kwargs):
        seen["cmd"] = cmd
        seen["kwargs"] = kwargs
        return subprocess.CompletedProcess(args=cmd, returncode=0, stdout="", stderr="")

    monkeypatch.setattr(main.subprocess, "run", fake_run)
    main._rasterize(tmp_path, tmp_path / "out.png")
    cmd = seen["cmd"]
    assert cmd[:3] == ["prlimit", f"--as={main.RASTERIZE_MEMORY_BYTES}", "--"]
    assert cmd[cmd.index("--root") + 1] == str(tmp_path)
    assert seen["kwargs"]["timeout"] == main.RASTERIZE_TIMEOUT_SECONDS
    assert seen["kwargs"]["cwd"] == tmp_path


@pytest.mark.parametrize(
    ("raw", "want"),
    [
        ("error: failed to parse SVG\n  ┌─ /tmp/tmpab12cd/in.svg:1:0", "error: failed to parse SVG\n  ┌─ in.svg:1:0"),
        ("error: cannot write /tmp/tmpab12cd/out.png", "error: cannot write out.png"),
    ],
)
def test_scrub_stderr_rasterize_paths(raw, want):
    assert main._scrub_stderr(raw) == want
