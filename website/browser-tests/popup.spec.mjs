import { expect, test } from "@playwright/test";

async function openPopup(page) {
  await page.goto("/");
  await page.keyboard.press("Control+k");
  await expect(page.locator("#command-center")).toBeVisible();
}

async function selectRow(page, row) {
  const target = Number(await row.getAttribute("data-index"));
  const current = Number(await page.locator(".result-row.selected").getAttribute("data-index"));
  for (let step = 0; step < Math.abs(target - current); step++) {
    await page.keyboard.press(target > current ? "ArrowDown" : "ArrowUp");
  }
  await expect(row).toHaveAttribute("aria-selected", "true");
}

test("starts with agents and switches resource views without mixing groups", async ({ page }) => {
  await openPopup(page);
  for (const type of ["agent", "workspace", "worktree", "repository"]) {
    const rows = page.locator(".result-row");
    expect(await rows.count()).toBeGreaterThan(0);
    expect(await rows.evaluateAll(elements => elements.map(row => row.dataset.type))).toEqual(
      Array(await rows.count()).fill(type),
    );
    await page.keyboard.press("Shift+Tab");
  }
  await page.locator("#command-search").fill("Galpon");
  await expect(page.locator('.result-row[data-type="workspace"]')).toHaveCount(1);
  await expect(page.locator('.result-row[data-type="repository"]')).toHaveCount(1);
  await expect(page.locator('.result-row[data-type="worktree"]')).toHaveCount(2);
});

test("separates an idle connected session from successful work and shows its delegation", async ({ page }) => {
  await openPopup(page);
  const active = page.locator('.result-row[data-type="agent"][data-id="agents-control"]');
  await expect(active.locator(".row-detail")).toContainText("idle");
  await expect(active.locator(".state-mark.completed")).toHaveCount(0);
  await expect(active.locator(".row-workspace")).toHaveText("Galpon");
  await expect(active.getByLabel("1 delegated agents")).toBeVisible();
  await expect(page.locator("#control-detail")).toContainText("connected");
  const titleBefore = await active.locator(".row-title").boundingBox();
  await page.keyboard.press("ArrowDown");
  expect((await active.locator(".row-title").boundingBox()).x).toBe(titleBefore.x);
  await expect(page.locator("#control-detail .detail-title")).toHaveText("Your command center");
});

test("workspace Tab expands agents in recent-use order and Enter opens a child", async ({ page }) => {
  await openPopup(page);
  await page.locator("#command-search").fill("Your command center");
  await page.keyboard.press("Enter");
  await page.keyboard.press("Control+k");
  await page.locator("#command-search").fill("Galpon");
  const workspace = page.locator('.result-row[data-type="workspace"][data-id="galpon"]');
  await expect(workspace).toHaveAttribute("aria-expanded", "false");
  await selectRow(page, workspace);
  await page.keyboard.press("Tab");
  await expect(workspace).toHaveAttribute("aria-expanded", "true");
  const children = page.locator('.result-row[data-workspace-parent="galpon"]');
  await expect(children.locator(".row-title")).toHaveText(["Your command center", "Agents under control"]);
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("ArrowLeft");
  await expect(children.first()).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("Enter");
  await expect(page.locator("#command-center")).toBeHidden();
  await expect(page.locator(".tab-button.active")).toHaveText("Your command center");
});

test("workspace expansion is independent, collapsible, and reset by search edits", async ({ page }) => {
  await openPopup(page);
  await page.keyboard.press("Shift+Tab");
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
  await expect(page.locator("[data-workspace-parent]")).toHaveCount(0);
  await expect(sandbox).toHaveAttribute("aria-selected", "true");
  await expect(sandbox).toHaveAttribute("aria-expanded", "false");
});

test("Tab exposes the same delegation and task as the Work Dock", async ({ page }) => {
  await openPopup(page);
  await page.keyboard.press("Tab");
  const records = page.locator('.result-row[data-type="delegation"]');
  await expect(records.locator(".row-title")).toHaveText(["Interface"]);
  await page.keyboard.press("ArrowDown");
  await expect(page.locator("#control-detail")).toContainText("#2 Add workspace expansion");
  await expect(page.locator("#control-detail")).toContainText("Adding Tab expansion");
  await page.keyboard.press("Enter");
  await expect(page.locator("#demo-note")).toContainText("browser-only delegation record");
  await expect(page.locator(".agent-button")).toHaveCount(3);
  await expect(page.locator("#command-center")).toBeVisible();
});

test("search highlights titles without treating markup or hidden IDs as content", async ({ page }) => {
  await openPopup(page);
  await page.locator("#command-search").fill("control");
  await expect(page.locator('.result-row[data-type="agent"] .row-title mark')).toHaveText("control");
  await page.locator("#command-search").fill("<img src=x onerror=alert(1)>");
  await expect(page.locator("#command-results img")).toHaveCount(0);
  await expect(page.locator(".result-row")).toHaveCount(0);
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Tab");
  await page.keyboard.press("Enter");
  await expect(page.locator("#command-center")).toBeVisible();
});

test("state updates preserve the nested selection without reporting idle as success", async ({ page }) => {
  await page.clock.install();
  await openPopup(page);
  await page.keyboard.press("Escape");
  await page.clock.pauseAt(new Date(Date.now() + 1000));
  await page.getByLabel("Send a message to the demo agent").fill("Show working state");
  await page.keyboard.press("Enter");
  await page.keyboard.press("Control+k");
  await page.keyboard.press("Shift+Tab");
  const workspace = page.locator('.result-row[data-type="workspace"][data-id="galpon"]');
  await selectRow(page, workspace);
  await page.keyboard.press("Tab");
  await page.keyboard.press("ArrowDown");
  const child = page.locator('.result-row[data-workspace-parent="galpon"][data-id="agents-control"]');
  await expect(child.locator(".state-mark.working")).toBeVisible();
  await page.clock.runFor(1000);
  await expect(child.locator(".row-detail")).toContainText("idle");
  await expect(child.locator(".state-mark.completed")).toHaveCount(0);
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
      expect(await footer.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true);
      expect(await popup.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true);
      for (const row of await popup.locator(".result-row").all()) {
        const rowBox = await row.boundingBox();
        expect(rowBox.x + rowBox.width).toBeLessThanOrEqual(box.x + box.width);
      }
    }
  });
}
