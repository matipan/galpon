import { expect, test } from "@playwright/test";

async function openPopup(page) {
  await page.keyboard.press("Control+k");
  const popup = page.getByRole("dialog", { name: "Command center" });
  await expect(popup).toBeVisible();
  await expect(popup).toHaveCSS("transform", "none");
  return popup;
}

async function expectUnclippedText(locator) {
  // Text-content assertions pass even when CSS replaces most letters with an
  // ellipsis. Check the actual text bounds in both browser layout engines.
  const clipped = await locator.evaluateAll((elements) => elements.flatMap((element) => {
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
  test(`complete demo names remain readable at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 960 });
    const popup = await openPopup(page);
    await expectUnclippedText(popup.locator(".row-title, .row-workspace"));
    await page.locator("#command-search").fill("Galpon");
    await page.keyboard.press("Tab");
    await expect(popup.locator('[data-workspace-parent="galpon"]')).toHaveCount(2);
    await expectUnclippedText(popup.locator(".row-title, .row-workspace"));
  });
}

test("long names use available row space instead of a fixed percentage", async ({ page }) => {
  const workspaceTitle = "Website visual regression tests";
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
  await expect(popup.locator(".row-title", { hasText: agentTitle }).first()).toBeVisible();
  await expectUnclippedText(popup.locator(".row-title, .row-workspace"));
  await page.locator("#command-search").fill(workspaceTitle);
  await page.keyboard.press("Tab");
  await expect(popup.locator("[data-workspace-parent]")).toHaveCount(1);
  await expectUnclippedText(popup.locator(".row-title, .row-workspace"));
});

for (const width of [375, 1440]) {
  test(`popup content fills the frame without dark gaps at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 960 });
    const popup = await openPopup(page);
    await expect(popup).toHaveCSS("padding", "0px");
    await expect(popup).toHaveCSS("background-color", "rgb(35, 36, 50)");
    await expect(popup.locator(".command-center")).toHaveCSS("background-color", "rgb(35, 36, 50)");
    const frame = await popup.boundingBox();
    const title = await popup.locator(".tui-titleline").boundingBox();
    const footer = await popup.locator(".tui-footer").boundingBox();
    const caption = await popup.locator(".popup-caption").boundingBox();
    expect(caption.y + caption.height).toBeLessThanOrEqual(title.y);
    expect(title.y).toBeCloseTo(frame.y + 1, 1);
    expect(footer.y + footer.height).toBeCloseTo(frame.y + frame.height - 1, 1);
    for (const selector of [".tui-titleline", ".tui-search", ".command-results", ".tui-footer", ".result-heading"]) {
      const band = await popup.locator(selector).first().boundingBox();
      expect(band.x, selector).toBeCloseTo(frame.x + 1, 1);
      expect(band.x + band.width, selector).toBeCloseTo(frame.x + frame.width - 1, 1);
    }
  });
}
