# opencode-1.18.18 fixtures

Recorded live against `opencode-ai@1.18.18` (npm) in a
`node:24.18.0-slim` container, talking to the host's local Ollama via
`--add-host=host.docker.internal:host-gateway`, model `gpt-oss:20b`.
Recorded by hand (same rationale as pi-0.84.1). No secrets to redact
(local Ollama, no key); session/message/part/call ids replaced with
`ses_SESSION*`/`msg_MESSAGE*`/`prt_PART*`/`call_CALL*` placeholders and
the recording tmpdir with `/work/ws`.

Provider config was `~/.config/opencode/opencode.json`:

```json
{
  "provider": {
    "ollama": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "http://host.docker.internal:11434/v1" },
      "models": { "gpt-oss:20b": {} }
    }
  }
}
```

Invocation shape (identical across fixtures):

```
opencode run --format json -m ollama/gpt-oss:20b "<prompt>"
```

- `happy.ndjson`: single step ending `reason:"stop"`, assistant text is a
  pi-style sentinel verdict line (opencode has no output-schema flag).
- `tool.ndjson`: real `write` then `read` tool calls; each `tool_use` part
  carries `tool`, `callID`, and `state{status,input,output,metadata,title,
  time}`; steps that call tools finish with `reason:"tool-calls"`.
- `error.ndjson`: baseURL pointed at a dead port. Single top-level
  `{"type":"error","error":{"name":"APIError","data":{message,isRetryable,
  metadata:{url}}}}` event and **exit 1** — unlike codex, opencode's exit
  code is a reliable failure signal (confirmed here and in the 5/5
  container-reliability protocol runs, all exit 0 on success).

Notes for the parser:

- Every event is `{"type","timestamp","sessionID","part":{...}}` except
  the top-level `error` event, which has `error` instead of `part`.
- Event `type` uses underscores (`step_start`, `tool_use`), the inner
  `part.type` uses hyphens (`step-start`, `step-finish`).
- `step_finish.part.tokens`: `{total,input,output,reasoning,
  cache:{write,read}}`; `cost` present (0 for local provider). No
  per-run aggregate — sum the step_finish events.
- Upstream reliability context: issue #31435 (empty JSONL in containers,
  exit 0) is open and its fix PR #31446 closed unmerged, but the repro
  there uses the official opencode container image + Vertex SSE; this
  environment (npm install in node:24.18.0-slim + openai-compatible)
  passed 5/5 consecutive runs. Treat empty stdout with exit 0 as a
  failure anyway.
