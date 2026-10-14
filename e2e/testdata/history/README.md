# Saved conversation fixtures

These sessions contain a completed turn, manual compaction, an image input, and
an image returned by an executed tool. They were saved by Pi 1.1.0, Claude Code
2.1.296, and Codex 0.162.1 with isolated settings and local mock model responses.
Runtime directory paths use `/fixture`.

`ARCHIVED_REPLY` must remain in saved history. It must not reach the target
model after compaction. `CROSS_COMPACT_SUMMARY`, `IMAGE_INPUT_SAVED`,
`IMAGE_TOOL_SAVED`, and both image blocks must reach the model. The target must
execute its own Galpon tool and keep these properties after restart.
