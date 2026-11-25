# Galpon website

galpon.dev is a static, browser-only demo of Galpon in the Herdr desktop app.
It looks and works like the Galpon layout of the app. It does not contact a
daemon or a model, open a terminal, or change files. All records are samples
in browser memory, and a reload starts again.

## What it shows

- **The Galpon sidebar:** the workspaces with their agents and terminals, the
  state of each agent (purple works, yellow needs you, green is ready, a ring
  is idle), and the pull requests grouped by the next step. A right click on
  an agent or a pull request opens its menu. **Ask an agent for a review…**
  sends a review request to the agent that you choose.
- **An agent tab:** the context bar, the conversation with work logs, result
  and request cards, code blocks with Copy, and the composer with the model,
  the thinking level and the context ring. A message plays a short scripted
  run. **Draft the release notes** waits for an answer to a question.
- **The panel:** Edits, Tasks and Agents, as in the agent tab of the app.
- **The Changes view:** a pane beside the agent with the branch, the file
  tree, a semantic diff with changed words and viewed marks.
- **The command center** and **New agent**.

The first agent, **Meet Galpon**, explains Galpon and gives the install
commands. Keep them the same as the Quick start and Herdr app sections of the
repository README.

## Look

The demo uses the look of the Galpon parts of the app (Taller): neutral
surfaces, hairline lines, Geist and Geist Mono, key chips and one accent. The
colors are the ones that the app shows with its default dark theme. The icons
in `icons/` are the `tl-*.svg` icons of the app mods; `fonts/` holds the Geist
fonts with their licenses.

## Keys

| Keys | Action |
| --- | --- |
| Ctrl+K | Open or close the command center |
| Ctrl+K in the command center | Actions of the selected agent |
| Ctrl+N in the command center, Ctrl+Alt+N | New agent |
| Alt+C | Show or hide the Changes view |
| Ctrl+Alt+J | Go to the next agent that waits for you |
| Enter / Shift+Enter in the composer | Send / new line |
| Shift+Tab in the composer | Change the thinking level |
| Esc | Close a popup or the Changes view; stop a run |

## Files

- `index.html`: the window: sidebar, panes and status bar.
- `styles.css`: the look and the narrow-screen layout.
- `data.js`: the sample workspaces, agents, conversations, pull requests and
  diff.
- `app.js`: drawing, the scripted runs, the popups and the keys.
- `browser-tests/`: Playwright tests of what a visitor can do.

## Run locally

```sh
npm run site:dev
```

The server listens on `0.0.0.0:43187`. There is no build step.

```sh
npm run test:site
```

runs the browser tests in Chromium and Firefox.

## Deployment

`wrangler.jsonc` serves `website/` as Cloudflare Workers Static Assets at
`galpon.dev`. A push to `main` that changes the website runs the browser tests
and then deploys.
