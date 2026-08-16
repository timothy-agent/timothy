# codex-0.147.0 fixtures

Recorded live against `@openai/codex@0.147.0` in a `node:24.18.0-slim`
container, talking to the host's local Ollama via
`--add-host=host.docker.internal:host-gateway`. Recorded by hand (same
rationale as pi-0.84.1). No secrets to redact (local Ollama, dummy key);
`thread_id` UUIDs replaced with the placeholder `THREAD_ID`.

Two provider paths were used, matching how the adapter's two shapes get
exercised:

- `happy.ndjson`, `error.ndjson`: custom provider via `CODEX_HOME/config.toml`
  (`[model_providers.timothy]` with `base_url` at Ollama's
  `http://host.docker.internal:11434/v1`, `env_key`, `wire_api = "responses"`
  — the only wire codex 0.147.0 accepts; `"chat"` is a hard config error),
  model `qwen3:30b-a3b`. happy carries a structured verdict in the
  `agent_message` (enforced via `--output-schema`); error points the base_url
  at an unreachable port and shows the retry `error` events plus terminal
  `turn.failed`.
- `tool.ndjson`: codex's built-in OSS provider (`--oss --local-provider
  ollama`, `CODEX_OSS_BASE_URL=http://host.docker.internal:11434/v1`), model
  `gpt-oss:20b` — the qwen models never emitted tool calls through codex, so
  this is the one path that produced real `command_execution`
  item.started/item.completed pairs.

Invocation shape (flags identical across fixtures apart from provider/model):

```
CODEX_HOME=/w/codex-home TIMOTHY_EXEC_API_KEY=dummy NO_COLOR=1 \
  codex exec --json -C /w/ws --dangerously-bypass-approvals-and-sandbox \
  --skip-git-repo-check -m <model> [--output-schema /w/schema.json] "<prompt>"
```

Notes for the parser:

- `reasoning` items and the "Model metadata for `<model>` not found" `error`
  ITEM (`item.completed` with `item.type == "error"`) are noise — distinct
  from the top-level `{"type":"error"}` event.
- Item ids are not contiguous (reasoning items consume ids).
- `turn.completed.usage` fields observed: `input_tokens`,
  `cached_input_tokens`, `cache_write_input_tokens`, `output_tokens`,
  `reasoning_output_tokens`. No cost field exists.
- codex exits 0 on success but its exit codes are unreliable upstream —
  `turn.failed`/`error` events are the failure signal.
