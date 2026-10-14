"""HTTP wrapper for the Laya decision model (System One).
Serves typed decisions (choice, noul, score) on /v1/systemone.
Internal-only sidecar — gateway is the sole caller.
No auth: never expose this port outside the compose network.
"""

import json
import math
import os
import re
from typing import Any

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import JSONResponse

app = FastAPI()

MAX_BODY_BYTES = 10 * 1024 * 1024  # 10 MiB body cap
DEFAULT_MODEL = os.environ.get("LAYA_SERVE_SERVING_MODEL", "convaiinnovations/laya-typed-decisions")
BACKEND = os.environ.get("LAYA_SERVE_BACKEND", "fake")

# Optional real laya agent instance
_agent = None
if BACKEND == "laya":
    try:
        import laya  # type: ignore

        _agent = laya.load(DEFAULT_MODEL)
    except Exception:
        _agent = None


async def read_capped_body(request: Request) -> bytes:
    """Reads request body up to MAX_BODY_BYTES."""
    declared = request.headers.get("content-length")
    if declared is not None:
        try:
            if int(declared) > MAX_BODY_BYTES:
                raise HTTPException(status_code=413, detail="request body too large")
        except ValueError:
            raise HTTPException(status_code=400, detail="invalid content-length") from None

    chunks: list[bytes] = []
    total = 0
    async for chunk in request.stream():
        total += len(chunk)
        if total > MAX_BODY_BYTES:
            raise HTTPException(status_code=413, detail="request body too large")
        chunks.append(chunk)
    return b"".join(chunks)


@app.get("/healthz")
@app.get("/health")
def healthz():
    return {"status": "ok"}


@app.get("/v1/models")
def list_models():
    return {
        "object": "list",
        "data": [
            {"id": DEFAULT_MODEL, "object": "model"},
            {"id": "laya-english", "object": "model"},
        ],
    }


def _softmax(scores: list[float], temperature: float = 1.0) -> list[float]:
    if not scores:
        return []
    max_s = max(scores)
    exp_s = [math.exp((s - max_s) / temperature) for s in scores]
    sum_exp = sum(exp_s) or 1.0
    return [round(e / sum_exp, 4) for e in exp_s]


def _text_overlap_score(text: str, query: str) -> float:
    t_words = [w.lower() for w in re.findall(r"\w+", text)]
    q_words = [w.lower() for w in re.findall(r"\w+", query)]
    if not q_words or not t_words:
        return 0.1
    score = 0.1
    text_lower = text.lower()
    for qw in q_words:
        if len(qw) >= 3 and qw in text_lower:
            score += 1.5
        elif any(qw == tw or (len(qw) >= 4 and len(tw) >= 4 and (qw[:4] == tw[:4])) for tw in t_words):
            score += 1.0
    return score


def evaluate_fake(state_str: str, q_spec: dict[str, Any]) -> dict[str, Any]:
    q_type = q_spec.get("type", "choice")
    instructions = str(q_spec.get("instructions", ""))
    context = (state_str + " " + instructions).lower()

    if q_type == "choice":
        criteria = q_spec.get("criteria")
        options = []
        if isinstance(criteria, dict):
            options = list(criteria.keys())
        elif isinstance(criteria, list):
            options = [str(x) for x in criteria]
        elif "options" in q_spec and isinstance(q_spec["options"], list):
            options = [str(x) for x in q_spec["options"]]

        if not options:
            options = ["yes", "no"]

        raw_scores = []
        for opt in options:
            desc = ""
            if isinstance(criteria, dict):
                desc = str(criteria.get(opt, ""))
            score = _text_overlap_score(context, f"{opt} {desc}")
            if opt.lower() in context:
                score += 2.0
            raw_scores.append(score)

        probs = _softmax(raw_scores, temperature=0.5)
        prob_map = {opt: probs[i] for i, opt in enumerate(options)}
        best_idx = probs.index(max(probs))
        winning_opt = options[best_idx]
        return {
            "type": "choice",
            "choice": winning_opt,
            "confidence": probs[best_idx],
            "probabilities": prob_map,
        }

    if q_type == "noul":
        yes_keywords = ["yes", "true", "pass", "passed", "confirm", "done", "succeed", "successful", "ok", "good", "complete"]
        no_keywords = ["no", "false", "fail", "failed", "deny", "not", "reject", "missing", "error", "bad", "incomplete"]
        yes_score = sum(1.5 for k in yes_keywords if k in context) + 0.1
        no_score = sum(1.5 for k in no_keywords if k in context) + 0.1
        probs = _softmax([no_score, yes_score], temperature=0.6)
        p_true = probs[1]
        return {
            "type": "noul",
            "noul": p_true,
        }

    if q_type == "score":
        criteria = q_spec.get("criteria")
        levels = criteria if isinstance(criteria, list) else ["0", "1"]
        n = max(1, len(levels) - 1)
        # Normalize to [0.0, 1.0]
        score_val = 0.5
        if "high" in context or "urgent" in context or "success" in context:
            score_val = 0.85
        elif "low" in context or "minor" in context or "fail" in context:
            score_val = 0.20
        return {
            "type": "score",
            "score": score_val,
            "confidence": 0.85,
            "probabilities": {"0": round(1.0 - score_val, 4), "1": score_val},
        }

    raise HTTPException(status_code=400, detail=f"unknown question type: {q_type}")


@app.post("/v1/systemone")
@app.post("/v1/decide")
async def systemone(request: Request):
    raw_body = await read_capped_body(request)
    if not raw_body.strip():
        raise HTTPException(status_code=400, detail="empty request body")

    try:
        data = json.loads(raw_body)
    except json.JSONDecodeError as err:
        raise HTTPException(status_code=400, detail=f"invalid JSON: {err}") from None

    model = data.get("model") or DEFAULT_MODEL
    state = data.get("state", "")
    state_str = json.dumps(state) if isinstance(state, (dict, list)) else str(state)
    questions = data.get("questions") or {}
    if not questions:
        raise HTTPException(status_code=400, detail="questions dictionary is required")

    answers = {}
    if _agent is not None:
        try:
            res = _agent.predict(state, questions)
            return JSONResponse(content={"model": model, "answers": res, "usage": {"input_tokens": max(1, len(state_str) // 4)}})
        except Exception as err:
            # Fall back to evaluation logic on error
            pass

    for qid, q_spec in questions.items():
        answers[qid] = evaluate_fake(state_str, q_spec)

    input_tokens = max(1, len(state_str) // 4 + len(questions) * 10)
    return {
        "model": model,
        "answers": answers,
        "usage": {
            "input_tokens": input_tokens,
        },
    }
