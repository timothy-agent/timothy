# cursor-2026.08.11 fixtures

Recorded live against `cursor-agent 2026.08.11` (login/subscription auth).
No secrets to redact.

Invocation shape:

```
cursor-agent -p --force --output-format stream-json --trust "<prompt>"
```

run in the process cwd (no `--workspace` needed), model via `--model <slug>`.

- `happy.ndjson`: full successful run (create+verify a file). Terminal
  `result` event carries `usage` (camelCase: inputTokens/outputTokens/
  cacheReadTokens/cacheWriteTokens) and no cost field - cost stays
  unreported (D-013).
- `badmodel.stderr`: an unrecognized `--model` value. stdout is completely
  empty (no events at all, not even `system/init`), the CLI exits non-zero,
  and the only failure signal is this stderr message. Exercises the
  runner's generic transport-death path (delegated.go's finishNoResult),
  not the adapter parser - the adapter never sees a line to parse here.

Notes for the parser:

- `tool_call` variant keys (`editToolCall`, `shellToolCall`, ...) sit under
  `tool_call` itself, not a flat `tool_name`/`args` shape - the started/
  completed events for one call share `call_id`, and the tool name comes
  from whichever variant key is present.
- `thinking` and `user` (echoed prompt) events are noise.
- Exit codes are unreliable on success per the harness's own known bug -
  a clean terminal `result` event wins over the process exit code.
