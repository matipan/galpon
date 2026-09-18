# Galpon website

This static site is an interactive, browser-only Galpon product demo. It reproduces the Herdr workspace, Pi conversation, Galpon command center, and Pi Work Dock without a daemon, terminal, model, or file access.

The initial agent shows local TODOs beside observed delegated work. Send any prompt to replay a deterministic delegation lifecycle. Press `Ctrl-Space`, then `d`, to collapse or expand the Work Dock with browser-safe keys.

The `+` tab keeps Herdr behavior: it opens a normal terminal beside the agents, not the New Agent form. The demo terminal uses the current agent placement to show that a shell and an agent can work with the same files. Use `Ctrl-N` or the Galpon command center to create an agent.

## Run locally

```sh
npm run site:dev
```

The server listens on `0.0.0.0:43187`. Open `http://<host-address>:43187` from another host.

## Test

```sh
npm run test:site
```

## Cloudflare

The site has no build step. `wrangler.jsonc` deploys `website/` with Cloudflare Workers Static Assets and maps the Worker to `galpon.dev`.

```sh
npm run site:deploy:dry
npm run site:deploy
```
