"""Tests for ecosystem_stages.py. Run in Docker:
docker run --rm -v ./:/src -w /src python:3.13-alpine \
  python -I -m unittest discover -s scripts/lib -p 'test_ecosystem_stages.py'
"""
import json
import os
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from io import StringIO

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import ecosystem_stages as st  # noqa: E402


def ev(kind, **payload):
    return {"kind": kind, "payload": payload}


HAPPY = [
    ev("mission.prepare_complete", test_cmd="composer test", failures=[]),
    ev("mission.turn", phase="discover", ok=True),
    ev("mission.turn", phase="plan", ok=True),
    ev("mission.plan_created"),
    ev("mission.phase_started", phase="build"),
    ev("mission.phase_started", phase="prove"),
    ev("mission.review_verdict", decision="approved", findings_only=False),
    ev("mission.pr_opened", url="https://github.com/timothy-agent/echo/pull/7", number=7),
]


class StagesTest(unittest.TestCase):
    def test_happy_path(self):
        stages, detail = st.stages({}, HAPPY)
        self.assertTrue(all(stages.values()), stages)
        self.assertEqual(detail["plan_sessions"], 1)
        self.assertEqual(detail["pr_url"], "https://github.com/timothy-agent/echo/pull/7")
        self.assertEqual(st.cause("done", {}, HAPPY, stages, detail), "")

    def test_rejected_first_plan_and_failed_baseline(self):
        events = [
            ev("mission.prepare_complete", failures=["no baseline test command succeeded; tried `x`"]),
            ev("mission.turn", phase="plan", ok=False, input="worker_failed", reason="gate"),
            ev("mission.turn", phase="plan", ok=True),
            ev("mission.plan_created"),
        ]
        stages, detail = st.stages({}, events)
        self.assertTrue(stages["prepare_ok"], "a baseline failure alone is not a prepare failure")
        self.assertFalse(stages["baseline_ok"])
        self.assertFalse(stages["plan_first_session"])
        self.assertEqual(detail["plan_sessions"], 2)

    def test_prepare_failure_skip_and_ceiling(self):
        for payload in ({"failures": ["mise install failed"]}, {"skipped": "no manifests"}, {"ceiling_hit": True}):
            stages, _ = st.stages({}, [ev("mission.prepare_complete", **payload)])
            self.assertFalse(stages["prepare_ok"], payload)
        stages, _ = st.stages({}, [])
        self.assertFalse(stages["prepare_ok"])

    def test_causes(self):
        stages, detail = st.stages({}, [])
        self.assertEqual(st.cause("timeout", {"phase": "build", "status": "working"}, [], stages, detail),
                         "timeout in build/working")
        self.assertEqual(st.cause("parked", {"pause_reason": "infra", "pause_message": "rate limit"}, [], stages, detail),
                         "parked: infra: rate limit")
        failed = [ev("mission.phase_started", phase="plan"), ev("mission.turn", phase="plan", ok=False, reason="no plan")]
        self.assertEqual(st.cause("failed", {"failure_reason": "max_iterations"}, failed, stages, detail),
                         "failed (max_iterations) in plan: no plan")
        delivery = [ev("mission.delivery_failed", reason="pr: 422")]
        self.assertEqual(st.cause("done", {}, delivery, stages, detail), "mission.delivery_failed: pr: 422")

    def test_main_writes_json_and_summary_line(self):
        with tempfile.TemporaryDirectory() as d:
            out = os.path.join(d, "go.json")
            env = {
                "M": json.dumps({"id": "m1", "phase": "done", "status": "idle"}),
                "EVENTS": json.dumps({"events": HAPPY}),
                "USAGE": json.dumps({"cost_by_currency": {"USD": 1.5}, "input_tokens": 10, "output_tokens": 2}),
                "OUTCOME": "done", "WALL": "120", "NAME": "go", "ECOSYSTEM": "go",
                "UPSTREAM": "labstack/echo", "FORK": "timothy-agent/echo", "SHA": "a" * 40,
                "CASE_NAME": "go-1234abcd", "RUN_TAG": "eco-x", "OUT": out,
            }
            old = dict(os.environ)
            os.environ.update(env)
            try:
                buf = StringIO()
                with redirect_stdout(buf):
                    st.main()
            finally:
                os.environ.clear()
                os.environ.update(old)
            line = buf.getvalue().rstrip("\n").split("\t")
            self.assertEqual(line[:7], ["go", "yes", "yes", "yes", "yes", "yes", "yes"])
            self.assertEqual(line[7:11], ["1.5000 USD", "120", "done", "m1"])
            with open(out, encoding="utf-8") as f:
                result = json.load(f)
            self.assertEqual(result["mission_id"], "m1")
            self.assertEqual(result["wall_secs"], 120)
            self.assertEqual(result["cause"], "")


if __name__ == "__main__":
    unittest.main()
