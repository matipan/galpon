# Galpón browser companion frontend

This directory contains a no-build, mobile-first browser client. It has no
terminal, editor, file browser, diff viewer, or workspace administration.
Dynamic transcript text is never interpreted as HTML. The safe Markdown
renderer builds headings, paragraphs, emphasis, lists and task lists,
blockquotes, fenced code, tables, images from authenticated Companion
attachments, and absolute HTTP links with DOM APIs. It assigns all source text
with `textContent` and does not render raw HTML. Wide code blocks and tables use
accessible scroll regions. Pi agent start, end, settled, and private reasoning
events stay out of the discussion. Consecutive tool-only
assistant messages stay in one compact work band until visible discussion text,
a new prompt, or a failure creates a user-visible boundary. The band shows
at most ten action rows before it scrolls, and each row can expand to show its
recorded input and output. Agent-to-agent requests and results appear as flat
`🤖` delivery rows with the safe sender title. They are collapsed by default;
direct Companion feedback remains a user message.

Agent detail shows one compact current-work summary above the discussion. It
selects a reported blocker first, then active or queued work, and names the
checkpoint in the closed state. Pointer devices get a small hover or focus
preview. Activating the summary opens a full work view in place of the
discussion and composer. This view includes nested work, checkpoints,
blockers, freshness, milestones, and progress counts. It is closed by default,
and it has a visible close control and Escape-key behavior. Mobile discussion
text, gaps, marks, and composer spacing are dense, but interactive controls
stay at least 44 pixels high and form inputs stay at 16 pixels.

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
per-agent drafts, load retries, compact and expanded work states, pointer
preview, keyboard focus and close behavior, mobile density and overflow, and
basic accessible names and landmarks.

## Browser operation

Companion saves one feedback draft per agent in browser storage. The message
composer starts as one compact row. It moves its controls below wrapped text,
grows to five text rows, and then scrolls. Enter adds a line, and Control-Enter
sends. The conversation follows new content only while it is near the end. If
the user scrolls up, it shows a jump-to-latest control. The Agent Work Dock
shows bounded active, blocked, completed, and nested delegated work. It keeps
request time limits, retries failed initial and detail loads in place, and
coalesces stream invalidations.
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
- `GET /api/v1/agents/{id}/operations` for bounded selected-agent Operations
- `GET /api/v1/events?after=N` as SSE, with `event: invalidate`
- `POST /api/v1/agents/{id}/messages`
- `POST /api/v1/agents/{id}/audio-messages` with multipart form fields `audio`, `language` (`en` or `es`), and optional `images`
- `POST /api/v1/agents`

Mutations send an `Idempotency-Key` header. Bootstrap sets `audioMessages` when
the host has both `voxtype` and `ffmpeg`. The voice control also requires the
browser MediaRecorder API. One `EN` or `ES` toggle stores the language for each
agent in browser storage. Bootstrap supplies existing workspaces and
repositories for a normal launch. Existing agents are offered
as an optional starting point only when `canCopyPlacement: true`.

The API calls are isolated in `api.mjs`. `mock-api.mjs` implements the same
small client interface for preview work.
