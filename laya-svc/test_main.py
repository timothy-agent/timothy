from fastapi.testclient import TestClient
from main import MAX_BODY_BYTES, app

client = TestClient(app)


def test_healthz():
    resp = client.get("/healthz")
    assert resp.status_code == 200
    assert resp.json() == {"status": "ok"}


def test_models():
    resp = client.get("/v1/models")
    assert resp.status_code == 200
    data = resp.json()
    assert "data" in data
    assert any("laya" in m["id"] for m in data["data"])


def test_choice_evaluation():
    req = {
        "state": "The user wants to write Go unit tests for a web service.",
        "questions": {
            "kind": {
                "type": "choice",
                "instructions": "What kind of mission is this?",
                "criteria": {
                    "coding": "Writing or testing software code",
                    "research": "Investigating papers or documentation",
                    "general": "Miscellaneous tasks",
                },
            }
        },
    }
    resp = client.post("/v1/systemone", json=req)
    assert resp.status_code == 200
    res = resp.json()
    assert res["model"]
    ans = res["answers"]["kind"]
    assert ans["type"] == "choice"
    assert ans["choice"] == "coding"
    assert ans["confidence"] > 0.4
    assert "coding" in ans["probabilities"]
    assert res["usage"]["input_tokens"] > 0


def test_noul_evaluation():
    req = {
        "state": "All tests passed with exit code 0.",
        "questions": {
            "done": {
                "type": "noul",
                "instructions": "Is the work complete and successful?",
            }
        },
    }
    resp = client.post("/v1/systemone", json=req)
    assert resp.status_code == 200
    res = resp.json()
    ans = res["answers"]["done"]
    assert ans["type"] == "noul"
    assert isinstance(ans["noul"], float)
    assert 0.0 <= ans["noul"] <= 1.0
    assert ans["noul"] > 0.5  # should lean yes/true


def test_score_evaluation():
    req = {
        "state": "System is experiencing high CPU and memory degradation.",
        "questions": {
            "urgency": {
                "type": "score",
                "instructions": "How urgent is this incident?",
            }
        },
    }
    resp = client.post("/v1/systemone", json=req)
    assert resp.status_code == 200
    res = resp.json()
    ans = res["answers"]["urgency"]
    assert ans["type"] == "score"
    assert isinstance(ans["score"], float)
    assert 0.0 <= ans["score"] <= 1.0


def test_empty_body_rejected():
    resp = client.post("/v1/systemone", content=b"")
    assert resp.status_code == 400


def test_invalid_json_rejected():
    resp = client.post("/v1/systemone", content=b"not json")
    assert resp.status_code == 400


def test_oversized_body_rejected():
    resp = client.post("/v1/systemone", content=b"x" * (MAX_BODY_BYTES + 1))
    assert resp.status_code == 413
