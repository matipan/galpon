import { expect, test } from "@playwright/test";

async function openPopup(page) {
  await page.keyboard.press("Control+k");
  const popup = page.locator("#command-center");
  await expect(popup).toBeVisible();
  return popup;
}

async function expectUnclippedText(locator) {
  const clipped = await locator.evaluateAll(elements => elements.flatMap(element => {
    if (!element.getClientRects().length) return [];
    const range = document.createRange();
    range.selectNodeContents(element);
    const needed = range.getBoundingClientRect().width;
    const available = element.getBoundingClientRect().width;
    return needed > available + .5 ? [{ text: element.textContent, needed, available }] : [];
  }));
  expect(clipped).toEqual([]);
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
});

for (const width of [800, 1440, 2000]) {
  test(`demo names remain readable at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 960 });
    const popup = await openPopup(page);
    await expectUnclippedText(popup.locator(".row-title, .row-workspace"));
    await page.locator("#command-search").fill("Galpon");
    await page.keyboard.press("Tab");
    await expect(popup.locator('[data-workspace-parent="galpon"]')).toHaveCount(2);
    await expectUnclippedText(popup.locator(".row-title, .row-workspace"));
  });
}

test("long names remain available in detail when the list must truncate them", async ({ page }) => {
  const workspaceTitle = "Website visual review";
  const agentTitle = "Investigate cross-browser popup layout and preserve complete labels";
  await page.keyboard.press("Control+Space");
  await page.keyboard.press("w");
  await page.getByRole("dialog", { name: "New workspace" }).getByLabel("Workspace title").fill(workspaceTitle);
  await page.keyboard.press("Control+s");
  await page.locator("#command-search").fill(workspaceTitle);
  await page.keyboard.press("Control+n");
  await page.getByRole("dialog", { name: "New agent" }).getByLabel("Name").fill(agentTitle);
  await page.keyboard.press("Control+s");
  const popup = await openPopup(page);
  await page.locator("#command-search").fill(agentTitle);
  await expect(popup.locator('.result-row[data-type="agent"] .row-title')).toHaveText(agentTitle);
  await page.keyboard.press("Control+g");
  const title = popup.locator(".detail-title");
  await expect(title).toHaveText(agentTitle);
  expect(await title.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true);
  await page.setViewportSize({ width: 375, height: 812 });
  await expect(title).toBeVisible();
  expect(await title.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true);
  await page.keyboard.press("Control+g");
  await expect(popup.getByRole("option", { selected: true })).toContainText(agentTitle);
});

for (const width of [375, 1440]) {
  test(`popup panels and keyboard hints stay within the frame at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 960 });
    const popup = await openPopup(page);
    const frame = await popup.boundingBox();
    for (const selector of [".tui-titleline", ".tui-search", ".command-results", ".tui-footer"]) {
      const band = await popup.locator(selector).boundingBox();
      expect(band.x, selector).toBeGreaterThan(frame.x);
      expect(band.x + band.width, selector).toBeLessThan(frame.x + frame.width);
      expect(band.y + band.height, selector).toBeLessThan(frame.y + frame.height);
    }
    const list = popup.locator(".command-results");
    const detail = popup.locator(".control-detail");
    if (width === 1440) {
      await expect(detail).toBeVisible();
      expect((await list.boundingBox()).width).toBeCloseTo((await detail.boundingBox()).width, 0);
    } else {
      await expect(detail).toBeHidden();
      await page.keyboard.press("Control+g");
      await expect(detail).toBeVisible();
      await expect(list).toBeHidden();
    }
  });
}
