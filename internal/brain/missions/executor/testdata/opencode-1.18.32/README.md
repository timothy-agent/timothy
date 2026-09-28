# opencode-1.18.32 fixtures

Recorded live against `opencode-ai@1.18.32` (npm) in a
`node:24.18.0-slim` container, talking to the host's local Ollama via
`--add-host=host.docker.internal:host-gateway`, model `gpt-oss:20b`.
Recorded by hand (same rationale as pi-0.87.1). No secrets to redact
(local Ollama, no key); session/message/part/call ids replaced with
`ses_SESSION*`/`msg_MESSAGE*`/`prt_PART*`/`call_CALL*` placeholders. The
recording tmpdirs (`/w/ws-happy`, `/w/ws-tool`) are container-relative,
not host paths, so they were left as-is.

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
  code is a reliable failure signal (confirmed here again on this bump).

Notes for the parser:

- Every event is `{"type","timestamp","sessionID","part":{...}}` except
  the top-level `error` event, which has `error` instead of `part`.
- Event `type` uses underscores (`step_start`, `tool_use`), the inner
  `part.type` uses hyphens (`step-start`, `step-finish`).
- `step_finish.part.tokens`: `{total,input,output,reasoning,
  cache:{write,read}}`; `cost` present (0 for local provider). No
  per-run aggregate — sum the step_finish events.
- No wire-format drift found between 1.18.18 and 1.18.32: event shapes,
  field names, and exit-code behavior all matched; the adapter needed
  no changes for this bump.
