"""HTTP wrapper around markitdown: converts one file to markdown per
request. Internal-only sidecar — brain is the sole caller, same trust
boundary as searxng. No auth: never expose this port outside the
compose network.
"""

import base64
import io

import fitz  # PyMuPDF
from fastapi import FastAPI, Header, HTTPException, Query, Request
from fastapi.responses import JSONResponse
from markitdown import MarkItDown, StreamInfo

app = FastAPI()
converter = MarkItDown(enable_plugins=False)

# Below this many extracted text characters, a page counts as scanned
# (image-only) and gets a rendered PNG so brain can caption/OCR it via
# the vision model instead of losing the page silently (issue #350).
DEFAULT_MIN_TEXT_CHARS = 200
# Embedded images smaller than this in either dimension are almost
# always decorative (icons, dividers, masks), not diagram content:
# skipping them keeps captioning spend on images worth describing.
MIN_IMAGE_DIM = 64
DEFAULT_RENDER_DPI = 110
DEFAULT_MAX_PAGES = 50
DEFAULT_MAX_IMAGES_PER_PAGE = 10


@app.get("/healthz")
def healthz():
    return {"status": "ok"}


@app.post("/convert")
async def convert(
    request: Request,
    x_filename: str | None = Header(default=None),
    x_mimetype: str | None = Header(default=None),
):
    """Converts the request body (raw file bytes) to markdown.

    The filename and MIME type ride request headers, not the body —
    the body is the file's bytes verbatim, nothing else to parse out.
    """
    body = await request.body()
    if not body:
        raise HTTPException(status_code=400, detail="empty request body")

    stream_info = StreamInfo(filename=x_filename, mimetype=x_mimetype)
    try:
        result = converter.convert_stream(io.BytesIO(body), stream_info=stream_info)
    except Exception as exc:  # markitdown raises assorted converter-specific errors
        raise HTTPException(status_code=422, detail=f"conversion failed: {exc}") from exc

    return JSONResponse({"markdown": result.markdown})


@app.post("/pdf/images")
async def pdf_images(
    request: Request,
    min_text_chars: int = Query(default=DEFAULT_MIN_TEXT_CHARS, ge=0),
    render_dpi: int = Query(default=DEFAULT_RENDER_DPI, ge=50, le=300),
    max_pages: int = Query(default=DEFAULT_MAX_PAGES, ge=1, le=200),
    max_images_per_page: int = Query(default=DEFAULT_MAX_IMAGES_PER_PAGE, ge=0, le=50),
):
    """Extracts embedded images and, for text-sparse (likely scanned)
    pages, a rendered page PNG from a raw PDF body: brain captions or
    OCRs these via the vision gateway (issue #350) since markitdown's
    own PDF converter (pdfminer, text-layer only) drops them silently.

    Per-page and per-image failures are swallowed and skipped: one
    corrupt image or unparsable page must never fail the whole
    document, only a wholly unparsable PDF returns 422.
    """
    body = await request.body()
    if not body:
        raise HTTPException(status_code=400, detail="empty request body")

    try:
        doc = fitz.open(stream=body, filetype="pdf")
    except Exception as exc:
        raise HTTPException(status_code=422, detail=f"pdf open failed: {exc}") from exc

    pages = []
    try:
        for page_index, page in enumerate(doc):
            if page_index >= max_pages:
                break
            pages.append(_extract_page(doc, page, min_text_chars, render_dpi, max_images_per_page))
    finally:
        doc.close()

    return JSONResponse({"pages": pages})


def _extract_page(doc, page, min_text_chars, render_dpi, max_images_per_page):
    page_number = page.number + 1
    try:
        text_chars = len(page.get_text())
    except Exception:
        text_chars = 0

    images = []
    try:
        for img in page.get_images(full=True)[:max_images_per_page]:
            extracted = _extract_image(doc, img[0])
            if extracted is not None:
                images.append(extracted)
    except Exception:
        pass

    render_b64 = None
    if text_chars < min_text_chars:
        try:
            pix = page.get_pixmap(dpi=render_dpi)
            render_b64 = base64.b64encode(pix.tobytes("png")).decode("ascii")
        except Exception:
            render_b64 = None

    return {
        "page": page_number,
        "text_chars": text_chars,
        "images": images,
        "render_b64": render_b64,
    }


def _extract_image(doc, xref):
    try:
        info = doc.extract_image(xref)
    except Exception:
        return None
    if not info:
        return None
    width, height = info.get("width", 0), info.get("height", 0)
    if width < MIN_IMAGE_DIM or height < MIN_IMAGE_DIM:
        return None
    ext = info.get("ext", "png")
    media_type = f"image/{'jpeg' if ext == 'jpg' else ext}"
    return {
        "index": xref,
        "media_type": media_type,
        "data_b64": base64.b64encode(info["image"]).decode("ascii"),
        "width": width,
        "height": height,
    }
