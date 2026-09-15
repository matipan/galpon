# Native Neovim Review prototype

## Scope

This prototype adds `/review nvim` and `/review nvim pick`. The existing
`/review` view remains available for comparison during the trial.

Neovim owns its terminal UI, source buffer, motions, search, selection, wrapping,
and comment editing. Galpon owns source identity, annotations, draft validation,
recovery, and the return to Pi. Preparing feedback must never send it.

Do not install or activate this prototype in a live Galpon session until the
user approves the trial installation.

## Isolation and setup

- This prototype supports Linux and Neovim 0.11 or newer.
- Reuse a compatible Neovim executable, not the user's configuration.
- Use a private HOME and XDG directories, no ShaDa, swaps, backups, modelines,
  plugin manager, language servers, providers, or user runtime/pack paths.
- Load only pinned Markdown rendering code and the two Markdown parsers.
- `galpon review setup` prepares these dependencies once in Galpon state. It
  must not start the daemon or materialize Pi extension assets.
- Review launch must not download or compile dependencies.
- Use the active palette from `internal/tui/theme.go`, including its fallback.

## Terminal handover

Use Pi's documented `ctx.ui.custom()` and `tui.stop()`/`tui.start()` handover.
Start Neovim asynchronously with inherited terminal descriptors, no shell, and
an isolated working directory. Restore Pi in all exit and error paths. Keep
normal Galpon work polling suspended while the review owns the terminal.

Normal mode shows formatted Markdown. Visual modes show source Markdown.
The source buffer is read-only. Comment buffers use normal Neovim editing.

## Native file contract, version 1

The launcher supplies these environment variables:

- `GALPON_REVIEW_NVIM_INPUT`: private input JSON file.
- `GALPON_REVIEW_NVIM_OUTPUT`: private atomic snapshot JSON file.
- `GALPON_REVIEW_NVIM_RUNTIME`: prepared dependency directory.

Input fields:

```typescript
{
  version: 1;
  runId: string;
  sourceEntryId: string;
  sourceHash: string;       // Original response hash, unchanged from Pi Review.
  sourceTextHash: string;   // SHA-256 of the normalized read-only buffer text.
  text: string;            // parseReviewBuffer(source.text), joined with LF.
  items: ReviewItem[];
  editing?: ReviewEditingDraft;
  graphemes: Array<{ line: number; startColumn: number; endColumn: number }>;
  palette: Record<string, string>;
  limits: { items: number; selectionBytes: number; draftBytes: number };
}
```

`ReviewItem` and `ReviewEditingDraft` retain the existing Pi field names.
Lines and columns are zero-based. Columns are UTF-16 source offsets, not cells
or UTF-8 bytes. End columns are exclusive. Neovim converts its byte coordinates
at the boundary. `graphemes` contains only multi-codepoint graphemes. Selection
capture expands an endpoint inside one of these graphemes to its outer boundary.
Block selections are rejected. No source text is evaluated as commands or Lua.

Output fields:

```typescript
{
  version: 1;
  runId: string;
  sourceEntryId: string;
  sourceHash: string;
  sourceTextHash: string;  // Binds offsets to the normalized buffer format.
  corePid: number;         // The Neovim core, which can differ from the TUI PID.
  revision: number;        // Positive, increases for each snapshot.
  status: "open" | "prepare" | "cancel";
  items: ReviewItem[];
  editing?: ReviewEditingDraft;
}
```

Write a temporary file and atomically rename it to the output file. Save
completed changes immediately. Save comment edits after a short delay and flush
on exit. Keep the previous valid snapshot if a write fails. Do not prepare or
capture selections if the source buffer no longer matches `sourceTextHash`.

The Pi bridge validates the entire snapshot before applying it. It checks run
and source identity, revision, size, ranges, grapheme boundaries, exact quotes,
unique item IDs, comments, and editing-item identity. Invalid snapshots must not
replace a valid draft. A `prepare` result needs a successful process exit and
explicit confirmation before replacing any unsent Pi editor text.

## Recovery

Validated snapshots are mirrored into the existing Pi Review draft entries.
Native run handles are private custom entries, not model-context messages.
A run handle is tied to its source and owning Pi session. An interrupted run's
atomic snapshot can be recovered only through a matching handle on the active
session branch. Recovery never prepares or sends a draft automatically.

The launcher also sets `GALPON_REVIEW_NVIM_RUN_ID`. On Linux, process checks use
this unique environment marker, not a PID alone. Pi allows the owned Neovim core
to flush its final snapshot after the TUI process exits. Bounded shutdown can
signal that core only when its run marker still matches. A valid snapshot is
recorded in Pi before its recovery files are removed.

This isolates configuration and saved editor state. It is not an operating
system sandbox; normal Neovim commands remain available.

## Controls to test

- Native motions, including `w`, `b`, `zz`, `zt`, `zb`, and `Ctrl-e`.
- `v`/`V`, then `c`: comment on the selected source.
- `c` in normal source mode: comment on the logical line.
- Comment buffer: `Esc`, then `Enter` saves the comment in Normal mode.
  Enter in Insert mode adds a line. Review does not bind `Ctrl-s`.
- `Tab`: source/annotation pane; `Enter` or `e`: edit an annotation.
- Annotation pane: `x` deletes; `u` restores an annotation change.
- `]a`/`[a`: next/previous annotation.
- `s` in the source or annotation pane: prepare and return to Pi.
- `q`: close and keep the draft, including an unfinished comment.

Wide terminals use source and annotation columns. Narrow terminals use stacked
panes. Tiny terminals use one main pane with `Tab` switching. On very short
terminals, an active comment uses that main window. Neovim retains its own minimum
screen dimensions; this prototype does not simulate an editor below them.

## Colors

The native view uses Galpon's active palette from `internal/tui/theme.go`, with
Tokyo Night Moon as the fallback. It applies the palette to source and annotation
panes, the comment prompt, the blue status line, line numbers, search matches,
completion menus, messages, Markdown headings, links, code, and icons. Inactive
panes keep the same background. No user colorscheme or extra theme plugin is loaded.
Only the pinned Markdown plugin is started; automatic plugin loading stays off.

## Verification

`go test ./internal/piagent -run 'TestNeovimReview' -count=1 -v` checks the bridge,
real Neovim keys, and real Pi/Neovim terminal ownership. The terminal fixture uses
an isolated Galpon build, private state, and a local mock model endpoint. It
checks that Review sends no model requests. It checks actual Markdown render marks
in Normal and Visual modes, supplied palette colors, and multiline comment input.
It also measures five command-to-first
native-frame samples in a PTY. These are not terminal-emulator paint measurements.

Dagger uses pinned Neovim 0.11.5. Local verification also uses installed Neovim
0.12.5. Building and testing this branch does not install or activate it.

Recorded checks for this prototype:

- `go test ./... -count=1`: passed.
- `go test ./e2e -count=1`: passed, with the local mock model.
- `go vet ./...`: passed.
- `dagger --x-release v1.0.0-beta.9 check`: all 5 checks and 495 tests passed.
- Focused native Review and setup race tests: passed.
- Three repeated focused bridge and terminal runs passed with Neovim 0.12.5.
- The full native terminal tests also passed with Neovim 0.11.5.

On this Linux workstation with Pi 0.85.1 and the small five-line test response,
command-to-first-native-frame time was about 46 ms with Neovim 0.12.5 and
61 ms with Neovim 0.11.5. These are five-sample PTY measurements with prepared
dependencies, not a general performance guarantee or a terminal-emulator paint
benchmark. Larger responses and other machines can have different results.
These replace the earlier measurements: the PTY fixture now answers terminal
queries correctly, and the Markdown renderer is started, not only configured.
