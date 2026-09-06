# pi-0.84.1 fixtures

Recorded live against `@earendil-works/pi-coding-agent@0.84.1` in a
`node:24.18.0-slim` container, talking to the host's local Ollama
(`qwen3:30b-a3b`, tools-capable) via `--add-host=host.docker.internal:host-gateway`
and an openai-completions `models.json` provider entry pointed at
`http://host.docker.internal:11434/v1`. `scripts/record-executor-fixture.sh`
targets claude-cli's single-invocation shape and wasn't cleanly
parameterizable for pi's multi-step container setup (write models.json,
npm install, then run) - these were recorded by hand instead. No
redaction of secrets was needed (local Ollama, dummy `apiKey`); the
session header's random UUID was replaced with the placeholder
`SESSION_ID` for the same reason claude's fixtures do it, and `cwd` was
already the container-relative `/w`, not a host path.

Invocation shape for `happy.ndjson`/`error.ndjson`/`no-verdict.ndjson`
(each fixture only differs in prompt/model endpoint), recorded before
`--mode rpc` (issue #358) replaced `--mode json` as the adapter's
default:

```
PI_CODING_AGENT_DIR=/w/pi-agent PI_OFFLINE=1 PI_SKIP_VERSION_CHECK=1 PI_TELEMETRY=0 NO_COLOR=1 \
  pi --mode json --no-extensions --no-skills --no-prompt-templates --no-themes --no-context-files --no-session --no-approve \
  --tools read,bash,edit,write,grep,find,ls --model timothy/qwen3:30b-a3b "<prompt>" \
  > run.ndjson 2> stderr.log
```

`--mode rpc`'s own invocation drops the trailing `"<prompt>"` argv
element (the prompt instead rides `{"type":"prompt","message":"..."}`
on stdin, PromptCommand's output) - `rpc.ndjson` below is the only
fixture recorded/built against that mode.

- `happy.ndjson` — prompt: "Create a file named hello.txt ... end your
  final message with a single line containing only this exact JSON
  object and nothing else: {"status":"DONE","note":"created
  hello.txt"}". No hand-patching needed: qwen3:30b-a3b followed the
  instruction on the first try, producing one `tool_execution_start`/
  `tool_execution_end` pair for the `write` tool and a final assistant
  message ending in the exact verdict JSON. Exit code 0.
- `error.ndjson` — `models.json` baseUrl pointed at a dead port
  (`host.docker.internal:19999`). Confirms the documented behavior:
  `--mode json` exits 0 even though every attempt's `message_end`/
  `agent_end` carries `stopReason: "error"` and
  `errorMessage: "Connection error."`. Three `agent_end` events have
  `willRetry: true` (pi's own auto-retry, 3 attempts), the fourth has
  `willRetry: false` and is the terminal one. Exit code 0 (observed;
  matches docs/facts: json mode's exit code is unaffected by
  stopReason error/aborted).
- `no-verdict.ndjson` — prompt asked a plain question with "Do not
  output any JSON." The model answered in prose with `stopReason:
  "stop"` and no trailing JSON object anywhere in the final message.
  Exit code 0.
- `rpc.ndjson` - hand-built, not recorded live (issue #358, mid-run
  steering): `--mode rpc`'s stream is identical to `--mode json` except
  for two extra line types this fixture adds around a `happy.ndjson`-
  shaped run: `response` (the per-command ack `--mode rpc` emits for
  each stdin command) and `queue_update` (emitted when a `steer`
  command is accepted). Both must parse as noise, same as any other
  unrecognized type; there is no `session` event difference from json
  mode.
