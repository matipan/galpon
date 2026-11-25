import { expect, test } from "@playwright/test";

// The demo of Galpon in the Herdr app: what a visitor sees and can do. The
// demo calls no model and changes no file, so each test starts from the
// sample records.

const sidebar = (page) => page.locator("#sidebar");
const card = (page) => page.locator("#card");
const agentRow = (page, title) => sidebar(page).locator(".side-row", { hasText: title });

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(card(page).locator(".pane-head")).toContainText("Meet Galpon");
});

test("the first agent explains Galpon and how to install it", async ({ page }) => {
  await expect(card(page)).toContainText("durable coding agents");
  await expect(card(page).locator("pre").filter({ hasText: "galpon herdr-app install" })).toBeVisible();
  await expect(sidebar(page)).toContainText("Cloud Engines");
  await expect(page.locator("#pulls")).toContainText("Needs you");
});

test("the command center finds an agent by its title and opens it", async ({ page }) => {
  await page.keyboard.press("Control+k");
  const input = page.locator("#ck-input");
  await expect(input).toBeFocused();
  await input.fill("scheduler");
  await expect(page.locator(".result")).toHaveCount(1);
  await page.keyboard.press("Enter");
  await expect(page.locator("#overlay")).toBeHidden();
  await expect(card(page).locator(".pane-head")).toContainText("Engine scheduler capacity");
});

test("a pull request number finds the pull request and its agent", async ({ page }) => {
  await page.keyboard.press("Control+k");
  await page.locator("#ck-input").fill("#212");
  const results = page.locator(".results");
  await expect(results).toContainText("Queue deliveries while an agent asks a question");
  await expect(results).toContainText("Fix the flaky Codex e2e test");
});

test("a message plays a demo run until the agent is ready", async ({ page }) => {
  await agentRow(page, "Docs site").click();
  const prompt = page.locator("#prompt");
  await prompt.fill("Add a page about screens");
  await prompt.press("Enter");
  await expect(card(page).locator(".bubble").last()).toHaveText("Add a page about screens");
  await expect(agentRow(page, "Docs site").locator(".mark")).toHaveClass(/working/);
  await expect(card(page)).toContainText("no model was called", { timeout: 15_000 });
  await expect(agentRow(page, "Docs site").locator(".mark")).toHaveClass(/ready/);
});

test("the Changes view shows the Git work of an agent beside its tab", async ({ page }) => {
  await agentRow(page, "Fix the flaky Codex e2e test").click();
  await page.keyboard.press("Alt+c");
  const changes = page.locator("#changes-card");
  await expect(changes).toBeVisible();
  await expect(changes.locator(".file-head").first()).toContainText("queue.go");
  await changes.locator(".view-btn").first().click();
  await expect(changes.locator(".ch-totals")).toContainText("1/3 viewed");
  await page.keyboard.press("Escape");
  await expect(changes).toBeHidden();
});

test("New agent creates an agent and opens it", async ({ page }) => {
  await page.keyboard.press("Control+Alt+n");
  await page.locator("#na-name").fill("Add a dark mode toggle");
  await page.getByRole("radio", { name: "No repository" }).click();
  await expect(page.locator(".form-summary")).toContainText("empty directory");
  await page.getByRole("button", { name: "Create agent" }).click();
  await expect(agentRow(page, "Add a dark mode toggle")).toBeVisible();
  await expect(card(page).locator(".pane-head")).toContainText("Add a dark mode toggle");
  await expect(page.locator("#prompt")).toBeFocused();
});

test("a pull request asks an agent for a review from its menu", async ({ page }) => {
  await page.locator(".pr-row", { hasText: "galpon#212" }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Ask an agent for a review…" }).click();
  await page.locator("#ck-input").fill("docs");
  await page.keyboard.press("Enter");
  await expect(page.locator(".toast")).toContainText("Asked Docs site for a review");
  await agentRow(page, "Docs site").click();
  await expect(card(page).locator(".bubble").last()).toContainText("Please review the pull request galpon#212");
});

test("a question of an agent takes an answer", async ({ page }) => {
  await agentRow(page, "Draft the release notes").click();
  await page.getByRole("button", { name: /Only user-facing changes/ }).click();
  await expect(card(page).locator(".bubble").last()).toHaveText("Only user-facing changes");
  await expect(card(page).locator(".question")).toHaveCount(0);
});

test.describe("on a phone", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("the sidebar opens from the status bar and closes when an agent opens", async ({ page }) => {
    await expect(sidebar(page)).not.toBeInViewport();
    await page.locator('[data-action="open-sidebar"]').click();
    await expect(sidebar(page)).toBeInViewport();
    await agentRow(page, "Docs site").click();
    await expect(card(page).locator(".pane-head")).toContainText("Docs site");
    await expect(sidebar(page)).not.toBeInViewport();
  });
});
