# Galpon website

This is a static, browser-only product demo. It shows Herdr tabs, Pi conversations,
Galpon Control, creation forms, Operations, and the Work Dock. It does not contact
a daemon or model, open a real terminal, fetch repositories, or change files.
All records and changes stay in browser memory. Refresh resets them.

## Interface

The Galpon surfaces follow the current local interface:

- `⌂` identifies Galpon; `▰` identifies a section. The shared icon definitions are
  in `interface.js`.
- Control starts with agents. Wide screens have equal list and detail panels.
  Focus fills the entire row. Child agents use continuous tree guides.
- Search checks human-facing titles only, across stable resource groups.
- State marks use amber for work and attention, green for confirmed success,
  red for failure, and muted colors for pending or idle records. A connected
  session is not a completed task.
- Creation forms use aligned fields and a live effects panel. Selection changes
  do not create a record. Enter on Start or Ctrl-S submits the form.
- Pi has a quiet identity header, plain messages, connected tool frames, a compact
  Work Dock, and model/reasoning/usage information. The displayed usage is sample
  data, not real spending. Tool frames use Galpon's three-cell indent and separate
  closing state mark. Shell duration stays inside the frame. Collapsed shell
  output shows the last five lines; Ctrl-O exposes the complete output.
- `console.css` styles the Galpon surfaces. `styles.css` retains the existing
  Herdr sidebar and tab styling. The website uses the current demo palette; it
  cannot read a visitor's local Omarchy or Pi theme.

The popup and Pi demo remain keyboard-driven. Herdr sidebar and tab buttons
support the mouse. On a narrow screen, Control uses one panel; Ctrl-G opens the
selected detail. Narrow creation forms put their effects below the fields.

## Controls

| Keys | Action |
| --- | --- |
| Ctrl-K | Open Control |
| Up / Down, Enter | Select and open a result |
| Shift-Tab | Change the resource view |
| Tab | Expand or collapse a workspace or delegation |
| Ctrl-G | Show or leave full detail |
| Ctrl-R in Control | Reorder agents by attention and recent use |
| Ctrl-Space | Change between search and actions |
| Ctrl-N | New agent |
| Ctrl-S | Add a repository, or submit the open form |
| Escape | Back or close |
| Ctrl-O in Pi | Expand or collapse tool results |

In actions mode: `a` creates an agent, `r` adds a repository, `R` adds a remote,
`w` creates a workspace, `o` opens sample Operations, `t` opens a sample shell,
and `d` toggles the Work Dock. A workspace's expanded agents use recent-use order.

The Work Dock is a short, fixed example: one completed task, one in progress,
one ready task, and one task blocked by the implementation. Interface is the
only active delegation and owns task #2. The DELEGATED heading and compact row
match Galpon: an activity mark, title, `[started · observed]`, and a checkpoint
marked `(reported)`. Control and Operations use that same assignment. There are no replay controls or timed state changes. The completed
task is visible by default so each task state is shown.

`/workdock history` shows or hides completed tasks; `/todos` shows task history;
`/workdock` toggles the dock. A prompt containing `failure` shows a clearly labeled
sample failed tool call without changing the tasks. No command in this
demo runs a real tool.

The `+` Herdr tab opens a sample shell beside the current agent. It does not
create an agent. Agent placement and context options update sample records only.

## Run locally

```sh
npm run site:dev
```

The server listens on `0.0.0.0:43187`. Open `http://localhost:43187` on that
machine, or `http://<host-address>:43187` from another host.

There is no build step. Use `agent-browser` for visual and keyboard review at
wide and narrow sizes. The existing Playwright files describe the earlier
interface and need revision before they can be used as acceptance checks for
this layout. This design review does not add the UI experiment to Dagger.

## Deployment

`wrangler.jsonc` serves `website/` as Cloudflare Workers Static Assets at
`galpon.dev`. The deployment workflow runs the website checks on changes pushed
to `main`. No deployment is needed for local review.
