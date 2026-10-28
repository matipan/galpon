# Galpón browser companion frontend

This directory contains a no-build, mobile-first browser client. It has no
terminal, editor, file browser, diff viewer, or workspace administration.
Dynamic transcript text is never interpreted as HTML. The safe Markdown
renderer builds headings, paragraphs, emphasis, lists and task lists,
blockquotes, fenced code, tables, images from authenticated Companion
attachments, and absolute HTTP links with DOM APIs. It assigns all source text
with `textContent` and does not render raw HTML. Wide code blocks and tables use
accessible scroll regions. Pi agent start, end, settled, and private reasoning
events stay out of the discussion. A completed compaction appears only as a
small “Conversation compacted” marker with its time. Compaction summaries and
start reasons never render, and the marker has no expand control. The original
session data stays unchanged. Consecutive tool-only
assistant messages stay in one compact work band until visible discussion text,
a new prompt, or a failure creates a user-visible boundary. The band shows
at most ten one-line summaries before it scrolls. Each summary has a tool icon
and a short action label. It can expand to show the tool name and its recorded
input and output. Touch devices retain 44-pixel tool controls. Agent-to-agent requests and results appear as flat
`⊶` delivery rows with the safe sender title. They are collapsed by default;
direct Companion feedback remains a user message.

Agent detail shows the discussion and the composer. Mobile discussion text,
gaps, marks, and composer spacing are dense, but interactive controls stay at
least 44 pixels high and form inputs stay at 16 pixels.

## Visual design

The Companion uses a retro-futuristic browser design, not a terminal copy.
It shares Galpon's semantic state colors, entity marks, full-row selection,
and blue statusline. The browser adds geometric headings, a fine background
grid, restrained light effects, and short transitions. The header and app icons
reuse the Galpon logo assets from `website/`. Conversation text uses the full
pane width on a quiet surface. Inputs use aligned fields and underlines, not rounded cards.
Phone navigation shows one pane at a time. Labels name fields, controls, and
states only. The Companion does not add explanatory copy, placeholder
examples, or success confirmations.

Bootstrap can supply the active workstation `palette` from `internal/tui/theme.go`.
Only named hexadecimal colors are accepted. Missing colors use the Tokyo Night
Moon fallback. Secondary text moves toward the host foreground color when the
terminal's muted colors have insufficient contrast on browser panels. Work in
progress uses the three flat bars of the Galpon mark in a traveling wave: a
working agent, a running tool, and a reply in progress. All indicators share
the document clock, so rebuilt rows do not restart the wave. A working agent
also shows a scan line under the discussion header. Reduced-motion settings
stop all animation, and the bars then show the stepped heights of the mark.

The display font is the local Space Grotesk Latin variable font (weights
400–700), distributed under the SIL Open Font License in `fonts/OFL.txt`.
The font comes from the [Space Grotesk project](https://github.com/floriankarsten/space-grotesk)
through the Google Fonts distribution. The application embeds and serves the
font itself. It makes no font request to a third party. Other character sets
use the system font fallback.

## Safe isolated preview

Serve only this static directory and enable mock mode:

```sh
python3 -m http.server 4173 --bind 127.0.0.1 --directory internal/companion/web
```

Then open:

```text
http://127.0.0.1:4173/?mock=1
```

Mock mode keeps all data in the browser process. It does not use the Galpón
Unix socket, daemon, database, Herdr session, Pi session, or a model endpoint.
Use the browser responsive-device view to test phone sizes. At wide desktop
sizes, the client uses a persistent agent list and discussion pane. Phone
navigation remains a list-to-detail flow.

Do not remove `?mock=1` unless the isolated companion backend is running. Real
mode uses `/api/v1` on the same origin.

## Static tests

```sh
node --test internal/companion/web/*.test.mjs
```

The test supplies an in-memory fetch and EventSource implementation. It opens
no socket and sends no network request.

## Browser tests

The Playwright suite checks the full browser UI against mock data or intercepted
local requests. Install its pinned development dependency and Chromium once:

```sh
npm ci
npx playwright install chromium
```

Then run the explicit browser-only command from the repository root:

```sh
npm run test:browser
```

This command starts the real Companion HTTP adapter on loopback with an
isolated temporary store and a backend that cannot contact a Galpón daemon, Pi,
Herdr, or a model endpoint. Mock mode and intercepted browser routes provide
the test data. It is separate from the Go and Dagger checks. To use an installed system Chromium instead of the
Playwright browser download, set `PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` to its
absolute path.

The suite covers list and detail navigation, direct-link Back behavior,
per-agent drafts, load retries, keyboard focus, mobile density and overflow,
and basic accessible names and landmarks.

## Browser operation

Companion saves one feedback draft per agent in browser storage. The message
composer starts as one compact row. It moves its controls below wrapped text,
grows to five text rows, and then scrolls. Enter adds a line, and Control-Enter
sends. The conversation follows new content only while it is near the end. If
the user scrolls up, it shows a jump-to-latest control. It keeps request time
limits, retries failed initial and detail loads in place, and coalesces stream
invalidations.
The application manifest makes the production
companion installable from browsers that support web applications. Browsers
can store static assets but must validate them before reuse because asset names
are stable across releases. API data uses `no-store`.

For local performance inspection, run
`window.__galponCompanionPerformance()` in browser developer tools. This returns
bounded request samples, long-task totals, and layout-shift totals. Companion
does not send this data to the host or a third party.

## Expected API

- `GET /api/v1/bootstrap`
- `GET /api/v1/agents/{id}?before=N&messageBefore=TOKEN` for bounded history pages
- `GET /api/v1/events?after=N` as SSE, with `event: invalidate`
- `POST /api/v1/agents/{id}/messages`
- `POST /api/v1/agents/{id}/audio-messages` with multipart form fields `audio`, `language` (`en` or `es`), and optional `images`
- `POST /api/v1/agents`

Mutations send an `Idempotency-Key` header. Bootstrap sets `audioMessages` when
the host has both `voxtype` and `ffmpeg`. The voice control also requires the
browser MediaRecorder API. One `EN` or `ES` toggle stores the language for each
agent in browser storage. Bootstrap supplies existing workspaces and
repositories for repository launches. A launch can instead use an empty private
managed directory with no repository. Existing agents are offered as an optional
starting point only when `canCopyPlacement: true`.

The API calls are isolated in `api.mjs`. `mock-api.mjs` implements the same
small client interface for preview work.
