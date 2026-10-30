// A fixed browser-only sample of Pi background tasks. Nothing runs and no file
// is read. The notice and the task screen follow the Pi module in
// internal/piagent/galpon-background.ts.
import { icons, escapeHTML } from "./interface.js?v=background-tasks";

export const TASKS_KEY = "alt+t";
const TASK_GLYPH = "◷";
const TASK_DIR = "/tmp/galpon-pi-tasks-Xk2v9q";
const AGENT_FOLDER = "~/.local/state/galpon/worktrees/sandbox-747ca24a/build-something-2fdf63c9";
const REVIEW_CHECKOUT = `${AGENT_FOLDER}/demo-app-1f6b0c2e`;

const TEST_OUTPUT = [
  "> demo-app@1.4.0 test",
  "> vitest run",
  "",
  ` RUN  v3.2.4 ${REVIEW_CHECKOUT}`,
  "",
  " ✓ src/cart.test.ts (12 tests) 41ms",
  " ✓ src/checkout.test.ts (9 tests) 63ms",
  " ✓ src/pricing.test.ts (11 tests) 28ms",
  " ✓ src/api/orders.test.ts (14 tests) 118ms",
  " ✓ src/api/payments.test.ts (7 tests) 96ms",
  " ✓ src/ui/cart-view.test.tsx (5 tests) 211ms",
  "",
  " Test Files  6 passed (6)",
  "      Tests  58 passed (58)",
  "   Duration  1.84s",
];

const CI_EVENTS = ["lint       pass  38s", "typecheck  pass  51s"];

/** The tasks of Build something: a finished test run and a CI monitor that still runs. */
export function createBackgroundSample(now = Date.now()) {
  const started = now - 6 * 60_000;
  return [
    {
      id: "b7a2d5f1", kind: "command", description: "unit tests", command: "cd ../demo-app-1f6b0c2e && npm test",
      status: "completed", exitCode: 0, startedAt: started, endedAt: started + 48_000, output: TEST_OUTPUT,
    },
    {
      id: "m4c19e07", kind: "monitor", description: "CI checks on pull request 12",
      command: "gh pr checks 12 --watch --interval 30 | grep --line-buffered -E 'pass|fail'",
      status: "running", startedAt: started + 3_000, output: CI_EVENTS,
    },
  ];
}

/** The review request, the calls that started the tasks, and the notice that reported them. */
export function backgroundTranscript() {
  return [
    { role: "user", text: "Review pull request 12. Run its tests and watch CI while you read the code." },
    {
      role: "tool", title: "Add worktree",
      result: [
        "{",
        `  "folder": "${AGENT_FOLDER}",`,
        "  \"worktree\": {",
        `    "path": "${REVIEW_CHECKOUT}",`,
        "    \"baseRef\": \"pull/12/head\",",
        "    \"sourceRemote\": \"origin\",",
        "    \"head\": \"9f2c1ab0d4e17c5b2a8e6f30c9d1b4a7e2f8c615\",",
        "    \"detached\": true",
        "  }",
        "}",
      ].join("\n"),
    },
    {
      role: "tool", command: "$ cd ../demo-app-1f6b0c2e && npm test",
      result: [
        "Command running in the background with ID b7a2d5f1.",
        `Output file: ${TASK_DIR}/b7a2d5f1.output`,
        "You get a notice when it ends. Do not sleep or poll its output: do other work or end your turn.",
      ].join("\n"),
    },
    {
      role: "tool", title: "Monitor", target: "CI checks on pull request 12",
      command: "$ gh pr checks 12 --watch --interval 30 | grep --line-buffered -E 'pass|fail'",
      result: [
        "Monitor m4c19e07 started: CI checks on pull request 12.",
        `Output file: ${TASK_DIR}/m4c19e07.output`,
        "Each stdout line arrives as a notice while you work. It stops after 30m. Do not sleep or poll: do other work or end your turn. Stop it with TaskStop.",
      ].join("\n"),
    },
    {
      role: "notice",
      notices: [
        { id: "b7a2d5f1", description: "unit tests", status: "completed", summary: "The command exited with code 0 after 48s.", tail: TEST_OUTPUT },
        { id: "m4c19e07", description: "CI checks on pull request 12", status: "running", lines: CI_EVENTS },
      ],
    },
    {
      role: "assistant",
      html: `
        <p>Pull request 12 passes its unit tests: 58 tests in 6 files. CI reports lint and typecheck as passed. I get a notice for each new check, so I continue to read the code and do not wait.</p>
        <p>The review checkout is an extra worktree in my agent folder. Galpon shows it in Control and removes it with me.</p>
        <p>Press <code>Alt-T</code> to see the output of my background tasks.</p>`,
    },
  ];
}

export function outputPath(task) {
  return `${TASK_DIR}/${task.id}.output`;
}

export function elapsed(ms) {
  const seconds = Math.max(0, Math.round(ms / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return seconds % 60 ? `${minutes}m${String(seconds % 60).padStart(2, "0")}s` : `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return minutes % 60 ? `${hours}h${String(minutes % 60).padStart(2, "0")}m` : `${hours}h`;
}

function statusText(task) {
  const exit = task.exitCode === undefined || task.exitCode === null ? "" : ` with exit code ${task.exitCode}`;
  switch (task.status) {
  case "running": return "is running";
  case "completed": return `completed${exit}`;
  case "failed": return `failed${exit}`;
  case "stopped": return "was stopped";
  case "timed_out": return "timed out";
  default: return task.status;
  }
}

export function taskState(task, now = Date.now()) {
  return task.status === "running"
    ? `running ${elapsed(now - task.startedAt)}`
    : `${statusText(task)} after ${elapsed((task.endedAt ?? now) - task.startedAt)}`;
}

/** All tasks, newest first, as on the task screen. */
export function sortedTasks(tasks = []) {
  return [...tasks].sort((left, right) => right.startedAt - left.startedAt);
}

export function runningCount(tasks = []) {
  return tasks.filter(task => task.status === "running").length;
}

/** The footer status of Pi while tasks run. */
export function tasksStatus(count) {
  return count ? `${TASK_GLYPH} ${count} background · ${TASKS_KEY}` : "";
}

function taskMark(task) {
  const [glyph, tone] = task.status === "running" ? [TASK_GLYPH, "accent"]
    : task.status === "completed" ? [icons.success, "completed"]
    : task.status === "stopped" ? [icons.stopped, "warning"]
    : [icons.failed, "failed"];
  return `<span class="state-mark ${tone}" aria-hidden="true">${glyph}</span>`;
}

/**
 * A notice message. Collapsed, it shows the first lines of an event and the
 * last lines of a command; Ctrl-O shows all lines.
 */
export function noticeMarkup(notices, expanded) {
  const lines = [`<div class="notice-title"><span aria-hidden="true">${TASK_GLYPH}</span>&nbsp; BACKGROUND</div>`];
  for (const notice of notices) {
    const state = notice.status === "running" ? "event" : notice.status.replace("_", " ");
    const tone = ["failed", "timed_out"].includes(notice.status) ? " failed" : notice.status === "stopped" ? " warning" : "";
    lines.push(`<div class="notice-task"><span class="notice-description">${escapeHTML(notice.description)}</span><span class="notice-state${tone}">${escapeHTML(`${notice.id} · ${state}`)}</span></div>`);
    if (notice.summary) lines.push(`<div class="notice-summary">${escapeHTML(notice.summary)}</div>`);
    const event = [...(notice.lines || []), ...(notice.omitted ? [`… ${notice.omitted} more lines in ${outputPath(notice)}`] : [])];
    const tail = notice.tail || [];
    const shownEvent = expanded ? event : event.slice(0, 3);
    const shownTail = expanded ? tail : tail.slice(-3);
    for (const line of [...shownEvent, ...shownTail]) lines.push(`<div class="notice-line">${escapeHTML(line)}</div>`);
    const hidden = event.length - shownEvent.length + tail.length - shownTail.length;
    if (hidden) lines.push(`<div class="notice-more"><kbd>ctrl+o</kbd> ${hidden} more lines</div>`);
  }
  return lines.join("");
}

/** The task list, or the output of the task that screen.taskId names. */
export function taskScreenMarkup(screen, tasks, now = Date.now()) {
  // The escape hint is also a button, for a screen without a keyboard.
  const escape = label => `<button type="button" class="task-key" data-task-action="escape">esc ${label}</button>`;
  const title = subject => `<header class="task-title"><strong>${icons.brand} GALPON</strong> / BACKGROUND TASKS${subject ? ` / ${escapeHTML(subject)}` : ""}</header>`;
  const task = tasks.find(item => item.id === screen.taskId);
  if (!task) {
    const rows = tasks.map((item, index) => {
      const chosen = index === screen.selected;
      return `<div class="task-row${chosen ? " selected" : ""}" role="option" aria-selected="${chosen}" data-task-id="${escapeHTML(item.id)}">`
        + `<span class="row-prefix" aria-hidden="true">${chosen ? icons.focus : ""}</span>${taskMark(item)}`
        + `<span class="task-text"><span class="task-description">${escapeHTML(item.description)}</span>`
        + `<span class="task-meta">${escapeHTML(`${item.id} · ${item.kind} · ${taskState(item, now)}`)}</span></span></div>`;
    }).join("");
    return `<div class="task-rule"></div>${title()}<div class="task-rule"></div>`
      + `<div class="task-list" role="listbox" aria-label="Background tasks">${rows}</div>`
      + `<footer class="task-hint">↑↓ select · enter output · ${escape("close")}</footer>`;
  }
  const kind = task.kind === "command" ? "background command" : "monitor";
  const body = task.output.length
    ? task.output.map(line => `<div>${escapeHTML(line)}</div>`).join("")
    : `<div class="task-empty">${task.status === "running" ? "No output yet." : "No output."}</div>`;
  return `${title(task.description)}`
    + `<div class="task-meta-line">${taskMark(task)} ${escapeHTML(`${task.id} · ${kind} · ${taskState(task, now)} · ${outputPath(task)}`)}</div>`
    + `<div class="task-command">$ ${escapeHTML(task.command.split("\n")[0])}</div><div class="task-rule"></div>`
    + `<div class="task-output" aria-label="Output of ${escapeHTML(task.description)}">${body}</div>`
    + `<footer class="task-hint">↑↓ scroll · PgUp/PgDn page · Home/End · ${escape(screen.listed ? "back" : "close")}</footer>`;
}
