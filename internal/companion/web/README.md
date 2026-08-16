# Galpón phone companion frontend

This directory contains a no-build, mobile-first browser client. It has no
terminal, editor, file browser, diff viewer, or workspace administration.
All dynamic transcript text is inserted with `textContent` so source output is
not interpreted as HTML.

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
node --test internal/companion/web/api.test.mjs
```

The test supplies an in-memory fetch and EventSource implementation. It opens
no socket and sends no network request.

## Expected API

- `GET /api/v1/bootstrap`
- `GET /api/v1/agents/{id}?before=N` for bounded history pages
- `GET /api/v1/events?after=N` as SSE, with `event: invalidate`
- `POST /api/v1/agents/{id}/messages`
- `POST /api/v1/agents`

Mutations send an `Idempotency-Key` header. New-agent sources are shown only
when bootstrap sets `canCopyPlacement: true`.

The API calls are isolated in `api.mjs`. `mock-api.mjs` implements the same
small client interface for preview work.
