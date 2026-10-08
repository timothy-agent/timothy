"""Per-stage result of one ecosystem smoke-matrix row (issue #1018).

Reads the mission, its events and its usage from the environment
(M, EVENTS, USAGE as JSON) plus row metadata, writes the full result
as JSON to OUT and prints one tab-separated summary line.
"""
import json
import os

BASELINE_FAILURE = "no baseline test command succeeded"


def payload(e):
    return e.get("payload") or {}


def first(events, kind):
    return next((e for e in events if e["kind"] == kind), None)


def stages(mission, events):
    prep = first(events, "mission.prepare_complete")
    prep_p = payload(prep) if prep else {}
    prep_failures = prep_p.get("failures") or []
    prepare_ok = bool(prep) and not prep_p.get("skipped") and not prep_p.get("ceiling_hit") \
        and not [f for f in prep_failures if not f.startswith(BASELINE_FAILURE)]
    baseline_ok = bool(prep_p.get("test_cmd"))

    plan_turns = [payload(e) for e in events
                  if e["kind"] == "mission.turn" and payload(e).get("phase") == "plan"]
    plan_created = first(events, "mission.plan_created") is not None
    plan_first = plan_created and bool(plan_turns) and plan_turns[0].get("ok") is True

    build_passed = any(e["kind"] == "mission.phase_started" and payload(e).get("phase") == "prove"
                       for e in events)
    verdicts = [payload(e).get("decision") for e in events if e["kind"] == "mission.review_verdict"]
    review_approved = "approved" in verdicts
    pr = [payload(e) for e in events if e["kind"] == "mission.pr_opened"]
    pr_url = pr[-1].get("url", "") if pr else ""

    return {
        "prepare_ok": prepare_ok,
        "baseline_ok": baseline_ok,
        "plan_first_session": plan_first,
        "build_passed": build_passed,
        "review_approved": review_approved,
        "pr_opened": bool(pr_url),
    }, {
        "prepare_failures": prep_failures,
        "plan_sessions": len(plan_turns),
        "review_rounds": len(verdicts),
        "pr_url": pr_url,
    }


def cause(outcome, mission, events, st, detail):
    """Names why the row stopped short of a PR, or "" when it opened one."""
    if st["pr_opened"]:
        return ""
    for kind in ("mission.delivery_failed", "mission.push_failed"):
        e = [payload(x) for x in events if x["kind"] == kind]
        if e:
            return f"{kind}: {e[-1].get('reason') or e[-1].get('error') or ''}".strip()
    if outcome == "timeout":
        return f"timeout in {mission.get('phase')}/{mission.get('status')}"
    if outcome == "parked":
        return f"parked: {mission.get('pause_reason') or mission.get('status')}: {mission.get('pause_message') or ''}".strip()
    if outcome == "failed":
        failed_turns = [payload(e) for e in events if e["kind"] == "mission.turn" and not payload(e).get("ok")]
        reason = failed_turns[-1].get("reason", "") if failed_turns else ""
        return f"failed ({mission.get('failure_reason') or 'unknown'}) in {last_phase(events)}: {reason}".strip()
    if not st["prepare_ok"] and detail["prepare_failures"]:
        return "prepare: " + "; ".join(detail["prepare_failures"])
    return "done without a PR"


def last_phase(events):
    phases = [payload(e).get("phase") for e in events if e["kind"] == "mission.phase_started"]
    return phases[-1] if phases else "discover"


def human_asks(events):
    kinds = ("mission.permission_requested", "mission.permission_denied", "mission.input_requested")
    return sum(1 for e in events if e["kind"] in kinds)


def cost_text(usage):
    parts = [f"{v:.4f} {k}" for k, v in sorted((usage.get("cost_by_currency") or {}).items())]
    parts += [f"{v:.4f} {k} unbilled" for k, v in sorted((usage.get("unbilled_cost_by_currency") or {}).items())]
    return ", ".join(parts) or "unknown"


def main():
    env = os.environ
    mission = json.loads(env["M"])
    events = json.loads(env["EVENTS"]).get("events") or []
    usage = json.loads(env.get("USAGE") or "{}")
    outcome = env["OUTCOME"]
    st, detail = stages(mission, events)
    why = cause(outcome, mission, events, st, detail)
    result = {
        "name": env["NAME"], "ecosystem": env["ECOSYSTEM"], "upstream": env["UPSTREAM"],
        "fork": env["FORK"], "sha": env["SHA"], "case": env["CASE_NAME"], "run_tag": env["RUN_TAG"],
        "mission_id": mission.get("id"), "outcome": outcome,
        "phase": mission.get("phase"), "status": mission.get("status"),
        "stages": st, **detail,
        "human_asks": human_asks(events),
        "cost_by_currency": usage.get("cost_by_currency") or {},
        "unbilled_cost_by_currency": usage.get("unbilled_cost_by_currency") or {},
        "input_tokens": usage.get("input_tokens"), "output_tokens": usage.get("output_tokens"),
        "wall_secs": int(env["WALL"]), "cause": why,
    }
    with open(env["OUT"], "w", encoding="utf-8") as f:
        json.dump(result, f, indent=2, sort_keys=True)
        f.write("\n")
    cells = [env["NAME"]] + ["yes" if st[k] else "no" for k in
                             ("prepare_ok", "baseline_ok", "plan_first_session", "build_passed",
                              "review_approved", "pr_opened")]
    cells += [cost_text(usage), env["WALL"], outcome, str(mission.get("id")),
              why.replace("\t", " ").replace("\n", " ")[:300]]
    print("\t".join(cells))


if __name__ == "__main__":
    main()
