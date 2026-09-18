import { expect, test } from "@playwright/test";

test.beforeEach(async ({ page }) => {
  await page.goto("/");
});

test("opens as a full-screen Galpon and Herdr experience", async ({ page }) => {
  await expect(page).toHaveTitle(/Galpon/);
  await expect(page.getByRole("heading", { name: "spaces" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Agents under control", exact: true })).toBeVisible();
  await expect(page.getByText("You do not create branches", { exact: false })).toBeVisible();
  await expect(page.locator(".agent-button")).toHaveCount(3);
  await expect(page.locator('.agent-button', { hasText: "Build something" })).toContainText("Sandbox");
  await expect(page.locator(".herdr")).toHaveCSS("background-color", "rgb(26, 27, 38)");
  await expect(page.getByRole("button", { name: "Agents under control", exact: true })).toHaveCSS("pointer-events", "auto");
  await expect(page.locator('.agent-button.active .agent-state')).toHaveText("○");
  await expect(page.locator('.agent-button.active .agent-state')).toHaveCSS("color", "rgb(158, 206, 106)");
  const unseenAgentIndicator = page.locator('.agent-button', { hasText: "Your command center" }).locator('.agent-state');
  await expect(unseenAgentIndicator).toHaveText("●");
  await expect(unseenAgentIndicator).toHaveCSS("color", "rgb(122, 162, 247)");
  await expect(page.locator('.space-button[data-workspace-id="galpon"] .space-led')).toHaveText("○");
  await expect(page.locator('.space-button[data-workspace-id="galpon"] .space-led')).toHaveCSS("color", "rgb(158, 206, 106)");
  await expect(page.locator('.space-button[data-workspace-id="sandbox"] .space-led')).toHaveText("●");
  await expect(page.locator('.space-button[data-workspace-id="sandbox"] .space-led')).toHaveCSS("color", "rgb(122, 162, 247)");
});

test("Herdr spaces and tabs support the mouse", async ({ page }) => {
  await page.locator('.space-button[data-workspace-id="sandbox"]').click();
  await expect(page.locator('.agent-button', { hasText: "Your command center" })).toBeVisible();
  await expect(page.locator('.space-button[data-workspace-id="sandbox"] .space-led')).toHaveText("○");
  await expect(page.locator('.space-button[data-workspace-id="sandbox"] .space-led')).toHaveCSS("color", "rgb(158, 206, 106)");
  await expect(page.locator(".tab-button.active")).toHaveText("Build something");
  await expect(page.getByText("I am a durable Pi agent", { exact: false })).toBeVisible();

  await page.locator('.space-button[data-workspace-id="galpon"]').click();
  await page.locator(".tab-button", { hasText: "Your command center" }).click();
  await expect(page.getByText("What can I do from the Galpon command center?", { exact: true })).toBeVisible();
  await expect(page.locator('.agent-button.active .agent-state')).toHaveCSS("color", "rgb(158, 206, 106)");
});

test("the plus tab opens a normal Herdr terminal in the agent placement", async ({ page }) => {
  await page.getByRole("button", { name: "Open a terminal beside the current tab" }).click();

  await expect(page.getByRole("dialog", { name: "New agent" })).toBeHidden();
  await expect(page.locator(".terminal-tab.active")).toHaveText("$ zsh");
  const terminal = page.getByRole("region", { name: "Herdr terminal" });
  await expect(terminal).toBeVisible();
  await expect(terminal).toContainText("Agents under control · exact agent placement");
  await expect(terminal).toContainText("This is a normal Herdr shell, not an agent.");
  await expect(terminal).toContainText("Changes from either tab are immediately visible in the other.");
  await expect(page.getByLabel("Send a message to the demo agent")).toBeHidden();

  await page.locator(".tab-button", { hasText: "Agents under control" }).click();
  await expect(page.getByLabel("Send a message to the demo agent")).toBeVisible();
  await expect(page.getByText("Galpon sits on top of Herdr", { exact: false })).toBeVisible();
});

test("renders the Work Dock with local TODOs and delegated work", async ({ page }) => {
  const dock = page.getByRole("region", { name: "Work Dock" });
  await expect(dock).toBeVisible();
  await expect(dock.locator(".dock-heading")).toContainText("Work Dock · 5 todos · 1 ready · 4 delegations");
  await expect(dock.locator(".dock-section").first()).toContainText("Todos (1/5) · 1 ready");
  await expect(dock.locator('[data-todo-id="2"]')).toContainText("Build the interactive site (building the browser demo)");
  await expect(dock.locator('[data-todo-id="3"]')).toContainText("Verify keyboard flows ⛓ #2");
  await expect(dock.locator(".dock-section").last()).toContainText("Delegations (3/4 active)");
  await expect(dock.locator('[data-work-id="work-website"]')).toContainText("[started · observed]");
  await expect(dock.locator('[data-work-id="work-website"]')).toContainText("working · Building the Work Dock · reported");
  await expect(dock.locator('[data-work-id="work-review"]')).toContainText("[completed · observed]");
  await expect(dock.locator('[data-work-id="work-documentation"]')).toContainText("operation waiting");
  await expect(dock.locator('[data-live="true"]')).toHaveCount(1);
});

test("collapses the Work Dock and replays its safe lifecycle", async ({ page }) => {
  const dock = page.getByRole("region", { name: "Work Dock" });
  await page.keyboard.press("Control+Space");
  await expect(page.getByRole("dialog", { name: "Command center" }).getByRole("button", { name: /dock/ })).toBeVisible();
  await page.keyboard.press("d");
  await expect(page.getByRole("dialog", { name: "Command center" })).toBeHidden();
  await expect(dock).toContainText("ctrl+space d to expand");
  await expect(dock.locator(".dock-todo")).toHaveCount(0);

  await page.keyboard.press("Control+Space");
  await page.keyboard.press("d");
  await expect(dock.locator(".dock-todo")).toHaveCount(5);

  await page.getByLabel("Send a message to the demo agent").fill("Replay the Work Dock");
  await page.getByLabel("Send a message to the demo agent").press("Enter");
  await expect(dock.locator(".dock-section").last()).toContainText("Delegations (4/4 active)");
  await expect(dock.locator('[data-work-id="work-website"]')).toContainText("[queued · observed]");
  await expect(dock.locator('[data-work-id="work-website"]')).toContainText("[waiting · observed]", { timeout: 2_500 });
  await expect(dock.locator('[data-work-id="work-review"]')).toContainText("verifying · Checking keyboard and layout behavior · reported");
  await expect(dock.locator('[data-todo-id="2"]')).toContainText("✓ #2 Build the interactive site", { timeout: 4_500 });
  await expect(dock.locator('[data-todo-id="3"]')).toContainText("◐ #3 Verify keyboard flows");
  expect(page.context().pages()).toHaveLength(1);
});

test("Ctrl-Space opens the command center and filters human-facing titles", async ({ page }) => {
  await page.keyboard.press("Control+Space");
  await page.keyboard.press("Control+Space");
  const dialog = page.getByRole("dialog", { name: "Command center" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("heading", { name: "WORKSPACES" })).toBeVisible();
  await expect(dialog.getByRole("heading", { name: "AGENTS" })).toBeVisible();
  await expect(dialog.getByRole("heading", { name: "WORKTREES" })).toBeVisible();
  await expect(dialog.getByRole("heading", { name: "REPOSITORIES" })).toBeVisible();

  await dialog.getByPlaceholder("Search titles…").fill("command");
  await expect(dialog.getByText("Your command center", { exact: true })).toBeVisible();
  await expect(dialog.getByText("Build something", { exact: true })).toHaveCount(0);
});

test("adds a repository and starts a durable demo agent", async ({ page }) => {
  await page.keyboard.press("Control+Space");
  await page.keyboard.press("r");

  const repositoryDialog = page.getByRole("dialog", { name: "Add repository" });
  await expect(repositoryDialog).toBeVisible();
  await repositoryDialog.getByLabel("Path or Git URL").fill("https://github.com/example/launch-site.git");
  await repositoryDialog.getByLabel("Title").fill("launch-site");
  await page.keyboard.press("Control+s");

  await expect(page.getByText("Repository launch-site is ready", { exact: false })).toBeVisible();
  const commandDialog = page.getByRole("dialog", { name: "Command center" });
  await expect(commandDialog).toBeVisible();
  await expect(commandDialog.getByText("launch-site", { exact: true })).toBeVisible();

  await page.keyboard.press("Control+Space");
  await page.keyboard.press("a");
  expect(page.context().pages()).toHaveLength(1);
  const agentDialog = page.getByRole("dialog", { name: "New agent" });
  await agentDialog.getByLabel("Name").fill("Ship the launch");
  await agentDialog.getByLabel("Role").fill("implementer");
  await agentDialog.getByLabel("Primary repository").selectOption({ label: "launch-site" });
  await expect(agentDialog.getByLabel("Type")).toHaveValue("worktree");
  await expect(agentDialog.getByText("Create agent and open Pi", { exact: true })).toBeVisible();
  await page.keyboard.press("Control+s");

  await expect(page.getByRole("button", { name: /Ship the launch/ }).first()).toBeVisible();
  await expect(page.getByText("private launch-site worktree", { exact: false })).toBeVisible();
});

test("switches guide agents and sends a safe demo prompt", async ({ page }) => {
  await page.keyboard.press("Control+Space");
  await page.keyboard.press("Control+Space");
  await page.getByRole("dialog", { name: "Command center" }).getByPlaceholder("Search titles…").fill("Your command center");
  await page.keyboard.press("Enter");
  await expect(page.getByText("What can I do from the Galpon command center?", { exact: true })).toBeVisible();

  await page.getByLabel("Send a message to the demo agent").fill("Can this edit my real files?");
  await page.getByLabel("Send a message to the demo agent").press("Enter");
  await expect(page.getByText("safe Galpon demo", { exact: false })).toBeVisible();
});

test("switches between search and action modes without running local operations", async ({ page }) => {
  await page.keyboard.press("Control+Space");
  const dialog = page.getByRole("dialog", { name: "Command center" });
  await expect(dialog.getByRole("button", { name: /term\/edit/ })).toBeVisible();
  await page.keyboard.press("t");
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("region", { name: "Herdr terminal" })).toContainText("exact agent placement");

  await page.keyboard.press("Control+Space");
  await page.keyboard.press("x");
  await expect(page.getByText("does not change durable state", { exact: false })).toBeVisible();

  await page.keyboard.press("Control+Space");
  await expect(dialog.getByRole("button", { name: /new agent/ })).toBeVisible();
});

test("has names for all visible controls", async ({ page }) => {
  const unnamed = await page.evaluate(() => [...document.querySelectorAll("button, input, textarea, select")]
    .filter((element) => {
      const style = getComputedStyle(element);
      if (style.display === "none" || style.visibility === "hidden" || !element.getClientRects().length) return false;
      const labels = element.labels ? [...element.labels].map((label) => label.textContent).join(" ") : "";
      return !(element.getAttribute("aria-label") || element.textContent.trim() || labels.trim() || element.getAttribute("title"));
    })
    .map((element) => element.outerHTML));
  expect(unnamed).toEqual([]);
});
