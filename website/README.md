# Galpon website

This static site is an interactive, browser-only Galpon product demo. It reproduces the Herdr workspace, Pi conversation, Galpon command center, and Pi Work Dock without a daemon, terminal, model, or file access.

The initial agent shows local TODOs beside observed delegated work. Send any prompt to replay a deterministic delegation lifecycle. Press `Ctrl-Space`, then `d`, to collapse or expand the Work Dock with browser-safe keys.

The `+` tab keeps Herdr behavior: it opens a normal terminal beside the agents, not the New Agent form. The demo terminal uses the current agent placement to show that a shell and an agent can work with the same files. Use `Ctrl-N` or the Galpon command center to create an agent.

The command center uses the local popup's flat section bands, state symbols,
inline workspace labels, compact rows, and blue frame. The initial list still
has three agents. Press `Tab` on a workspace to show its agents in recent-use
order. Press it again to collapse the list. Search edits close expanded lists.
The robot badges use the existing Work Dock data. Press `Tab` on a parent to
show those delegation records; `Enter` explains that they are browser-only.
These controls do not create agents, contact a model, or access local files.

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

Pushes to `main` that change the website, Wrangler configuration, or Node lockfiles run `.github/workflows/deploy-website.yml`. The workflow installs Chromium, runs the isolated website tests, and deploys only after they pass. It requires the `CLOUDFLARE_API_TOKEN` repository secret and the `CLOUDFLARE_ACCOUNT_ID` repository variable. Create the token with Cloudflare's **Edit Cloudflare Workers** template and restrict it to the deployment account.

For a manual deployment:

```sh
npm run site:deploy:dry
npm run site:deploy
```
