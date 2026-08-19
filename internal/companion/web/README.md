# Galpón phone companion frontend

This directory contains a no-build, mobile-first browser client. It has no
terminal, editor, file browser, diff viewer, or workspace administration.
Dynamic transcript text is never interpreted as HTML. The limited rich-text
renderer builds paragraphs, lists, code, and absolute HTTP links with DOM APIs
and assigns all source text with `textContent`. Pi agent start, end, and settled boundaries stay out
of the discussion. Available Pi reasoning appears in a separate disclosure.
Tool calls from one user turn appear as one compact work band. The band shows
at most ten action rows before it scrolls, and each row can expand to show its
recorded input and output.

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
Use the browser responsive-device view to test phone sizes.

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

This command starts a loopback static server. It does not contact a Galpón
daemon, Pi, Herdr, a model endpoint, or an application API. It is separate from
the Go and Dagger checks. To use an installed system Chromium instead of the
Playwright browser download, set `PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` to its
absolute path.

The suite covers list and detail navigation, direct-link Back behavior,
per-agent drafts, load retries, keyboard focus, mobile overflow, and basic
accessible names and landmarks.

## Browser operation

Companion saves one feedback draft per agent in browser storage. It keeps
request time limits, retries failed initial and detail loads in place, and
coalesces stream invalidations. The application manifest makes the production
companion installable from browsers that support web applications. Static
assets use a short private cache while HTML and API data always revalidate or
use `no-store`.

For local performance inspection, run
`window.__galponCompanionPerformance()` in browser developer tools. This returns
bounded request samples, long-task totals, and layout-shift totals. Companion
does not send this data to the host or a third party.

## Expected API

- `GET /api/v1/bootstrap`
- `GET /api/v1/agents/{id}?before=N&messageBefore=TOKEN` for bounded history pages
- `GET /api/v1/events?after=N` as SSE, with `event: invalidate`
- `POST /api/v1/agents/{id}/messages`
- `POST /api/v1/agents/{id}/audio-messages` with multipart form fields `audio` and `language` (`en` or `es`)
- `POST /api/v1/agents`

Mutations send an `Idempotency-Key` header. Bootstrap sets `audioMessages` when
the host has both `voxtype` and `ffmpeg`. The voice control also requires the
browser MediaRecorder API. One `EN` or `ES` toggle stores the language for each
agent in browser storage. Bootstrap supplies existing workspaces and
repositories for a normal launch. Existing agents are offered
as an optional starting point only when `canCopyPlacement: true`.

The API calls are isolated in `api.mjs`. `mock-api.mjs` implements the same
small client interface for preview work.
