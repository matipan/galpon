import { expect, test } from "@playwright/test";

async function openPopup(page) {
  await page.goto("/");
  await page.keyboard.press("Control+k");
  await expect(page.getByRole("dialog", { name: "Command center" })).toBeVisible();
  await expect(page.locator("#command-center")).toHaveCSS("transform", "none");
}

async function selectRow(page, row) {
  const target = Number(await row.getAttribute("data-index"));
  const current = Number(await page.locator(".result-row.selected").getAttribute("data-index"));
  for (let step = 0; step < Math.abs(target - current); step++) {
    await page.keyboard.press(target > current ? "ArrowDown" : "ArrowUp");
  }
  await expect(row).toHaveAttribute("aria-selected", "true");
}

test("uses the local popup frame, compact rows, and colored section bands", async ({ page }) => {
  await openPopup(page);
  const popup = page.locator("#command-center");
  await expect(popup).toHaveCSS("border-top-color", "rgb(122, 162, 247)");
  await expect(popup).toHaveCSS("box-shadow", "none");
  await expect(popup.locator(".popup-caption")).toHaveText("popup");
  for (const [type, color, symbol] of [
    ["agent", "rgb(68, 157, 171)", "●"],
    ["workspace", "rgb(173, 142, 230)", "▦"],
    ["worktree", "rgb(68, 157, 171)", "⑂"],
    ["repository", "rgb(235, 146, 123)", "⌂"],
  ]) {
    const heading = popup.locator(`.result-group[data-type="${type}"] .result-heading`);
    await expect(heading).toHaveCSS("background-color", "rgb(40, 42, 56)");
    await expect(heading.locator(".result-heading-label")).toHaveCSS("background-color", color);
    await expect(heading.locator(".result-heading-label")).toHaveCSS("color", "rgb(26, 27, 38)");
    await expect(heading.locator(".result-heading-icon")).toHaveText(symbol);
  }
  for (const selector of [".tui-titleline", ".result-heading", ".result-row", ".tui-footer"]) {
    expect((await popup.locator(selector).first().boundingBox()).height, selector).toBe(20);
  }
  expect((await popup.locator(".tui-search").boundingBox()).height).toBe(40);
  await expect(popup.locator(".tui-footer kbd").first()).toHaveCSS("padding", "0px");
  await expect(popup.locator(".tui-footer")).toHaveCSS("font-size", "14px");
  await expect(page.locator(".agent-button")).toHaveCount(3);
});

test("shows state symbols, inline workspace labels, and existing delegation counts", async ({ page }) => {
  await openPopup(page);
  const active = page.locator('.result-row[data-type="agent"][data-id="agents-control"]');
  await expect(active.locator(".row-marker")).toHaveText("●");
  await expect(active.locator(".row-marker")).toHaveCSS("color", "rgb(158, 206, 106)");
  await expect(active.locator(".row-workspace")).toHaveText("[Galpon]");
  await expect(active.locator(".row-workspace")).toHaveCSS("color", "rgb(173, 142, 230)");
  await expect(active.locator(".row-workspace")).toHaveCSS("font-weight", "700");
  await expect(active.locator(".row-delegated")).toHaveText("🤖 3");
  await expect(active.locator(".row-delegated")).toHaveCSS("color", "rgb(224, 175, 104)");
  await expect(active.locator(".row-detail")).toHaveText("guide · active");
  await expect(page.locator("#resource-counts")).toHaveText("2 workspaces · 3 worktrees · 3 agents · 4 delegated");
  const idle = page.locator('.result-row[data-type="agent"][data-id="sandbox-agent"]');
  await expect(idle.locator(".row-marker")).toHaveText("○");
  await expect(idle.locator(".row-marker")).toHaveCSS("color", "rgb(86, 95, 137)");
  await expect(page.locator('.result-row[data-type="worktree"]').first().locator(".row-title")).toHaveText("Galpon · galpon · Agents under control");
  await expect(page.locator('.result-row[data-type="worktree"]').first().locator(".row-detail")).not.toContainText("galpon/galpon/");
  const titleBefore = await active.locator(".row-title").boundingBox();
  await page.keyboard.press("ArrowDown");
  expect((await active.locator(".row-title").boundingBox()).x).toBe(titleBefore.x);
});

test("workspace Tab expands agents in recent-use order and Enter opens a child", async ({ page }) => {
  await openPopup(page);
  await page.locator("#command-search").fill("Your command center");
  await page.keyboard.press("Enter");
  await page.keyboard.press("Control+k");
  await page.locator("#command-search").fill("Galpon");
  const workspace = page.locator('.result-row[data-type="workspace"][data-id="galpon"]');
  await expect(workspace.locator(".row-marker")).toHaveText("▸");
  await expect(workspace.locator(".row-detail")).toHaveText("2 agents · tab to expand");
  await selectRow(page, workspace);
  await page.keyboard.press("Tab");
  await expect(workspace).toHaveAttribute("aria-expanded", "true");
  await expect(workspace.locator(".row-marker")).toHaveText("▾");
  await expect(workspace.locator(".row-detail")).toHaveText("2 agents · tab to collapse");
  const children = page.locator('.result-row[data-workspace-parent="galpon"]');
  await expect(children).toHaveCount(2);
  await expect(children.locator(".row-title")).toHaveText(["Your command center", "Agents under control"]);
  await expect(page.locator('.result-group[data-type="workspace"] .result-heading')).toHaveCount(1);
  await expect(page.locator('.result-group[data-type="agent"]')).toHaveCount(0);
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("ArrowLeft");
  await expect(children.first()).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("Enter");
  await expect(page.locator("#command-center")).toBeHidden();
  await expect(page.locator(".tab-button.active")).toHaveText("Your command center");
});

test("workspace expansion is independent, collapsible, and reset by search edits", async ({ page }) => {
  await openPopup(page);
  const galpon = page.locator('.result-row[data-type="workspace"][data-id="galpon"]');
  const sandbox = page.locator('.result-row[data-type="workspace"][data-id="sandbox"]');
  await selectRow(page, galpon);
  await page.keyboard.press("Tab");
  await selectRow(page, sandbox);
  await page.keyboard.press("Tab");
  await expect(page.locator('.result-row[data-workspace-parent="galpon"]')).toHaveCount(2);
  await expect(page.locator('.result-row[data-workspace-parent="sandbox"]')).toHaveCount(1);
  await selectRow(page, galpon);
  await page.keyboard.press("Tab");
  await expect(page.locator('.result-row[data-workspace-parent="galpon"]')).toHaveCount(0);
  await expect(sandbox).toHaveAttribute("aria-expanded", "true");
  await page.locator("#command-search").fill("Sandbox");
  await expect(page.locator('[data-workspace-parent]')).toHaveCount(0);
  await expect(sandbox).toHaveAttribute("aria-selected", "true");
  await expect(sandbox).toHaveAttribute("aria-expanded", "false");
});

test("Tab shows only the existing demo delegation records", async ({ page }) => {
  await openPopup(page);
  await page.keyboard.press("Tab");
  const records = page.locator('.result-row[data-type="delegation"]');
  await expect(records.locator(".row-title")).toHaveText(["Website", "Documentation", "Deployment"]);
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Tab");
  await expect(records.locator(".row-title")).toHaveText(["Website", "Interface review", "Documentation", "Deployment"]);
  await page.keyboard.press("Enter");
  await expect(page.locator("#demo-note")).toContainText("browser-only delegation record");
  await expect(page.locator(".agent-button")).toHaveCount(3);
  await expect(page.locator("#command-center")).toBeVisible();
});

test("search highlights titles without treating markup or hidden IDs as content", async ({ page }) => {
  await openPopup(page);
  await page.locator("#command-search").fill("control");
  await expect(page.locator('.result-row[data-type="agent"] .row-title mark')).toHaveText("control");
  await expect(page.locator(".row-title mark").first()).toHaveCSS("color", "rgb(68, 157, 171)");
  await page.locator("#command-search").fill("<img src=x onerror=alert(1)>");
  await expect(page.locator("#command-results img")).toHaveCount(0);
  await expect(page.locator(".result-row")).toHaveCount(0);
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Tab");
  await page.keyboard.press("Enter");
  await expect(page.locator("#command-center")).toBeVisible();
});

test("state updates keep a nested selection and use the local state colors", async ({ page }) => {
  await page.clock.install();
  await openPopup(page);
  await page.keyboard.press("Escape");
  await page.clock.pauseAt(new Date(Date.now() + 1000));
  await page.getByLabel("Send a message to the demo agent").fill("Show working state");
  await page.keyboard.press("Enter");
  await page.keyboard.press("Control+k");
  await page.clock.runFor(120);
  const workspace = page.locator('.result-row[data-type="workspace"][data-id="galpon"]');
  await selectRow(page, workspace);
  await page.keyboard.press("Tab");
  await page.keyboard.press("ArrowDown");
  const child = page.locator('.result-row[data-workspace-parent="galpon"][data-id="agents-control"]');
  await expect(child.locator(".row-marker")).toHaveText("◐");
  await expect(child.locator(".row-marker")).toHaveCSS("color", "rgb(224, 175, 104)");
  await page.clock.runFor(900);
  await expect(child.locator(".row-marker")).toHaveText("●");
  await expect(child.locator(".row-marker")).toHaveCSS("color", "rgb(158, 206, 106)");
  await expect(child).toHaveAttribute("aria-selected", "true");
  await expect(workspace).toHaveAttribute("aria-expanded", "true");
});

test("new titles remain text in the styled popup", async ({ page }) => {
  await page.goto("/");
  await page.keyboard.press("Control+n");
  await page.getByRole("dialog", { name: "New agent" }).getByLabel("Name").fill('<img src=x onerror="alert(1)">');
  await page.keyboard.press("Control+s");
  await page.keyboard.press("Control+k");
  await page.locator("#command-search").fill("onerror");
  await expect(page.locator('.result-row[data-type="agent"] .row-title')).toHaveText('<img src=x onerror="alert(1)">');
  await expect(page.locator("#command-results img")).toHaveCount(0);
  await expect(page.locator('.result-row[data-type="agent"] .row-title mark')).toHaveText("onerror");
});

for (const width of [375, 800, 1440, 2000]) {
  test(`popup fits its frame at ${width}px without clipped footer controls`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await openPopup(page);
    const popup = page.locator("#command-center");
    const box = await popup.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width);
    for (const mode of ["search", "actions"]) {
      if (mode === "actions") await page.keyboard.press("Control+Space");
      const footer = popup.locator(".tui-footer");
      expect(await footer.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
      expect(await popup.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
      for (const row of await popup.locator(".result-row").all()) {
        const rowBox = await row.boundingBox();
        expect(rowBox.x + rowBox.width).toBeLessThanOrEqual(box.x + box.width);
      }
    }
  });
}
