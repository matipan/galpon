import { expect, test } from "@playwright/test";

test("tablet list uses a centered readable measure", async ({ page }) => {
  await page.setViewportSize({ width: 768, height: 1024 });
  await page.goto("/?mock=1");
  await expect(page.getByRole("button", { name: /Mobile companion/ })).toBeVisible();
  const metrics = await page.evaluate(() => ({
    screen: document.querySelector("#agents-screen").getBoundingClientRect().width,
    search: document.querySelector(".search-band").getBoundingClientRect().width,
  }));
  expect(metrics.screen).toBeLessThanOrEqual(704);
  expect(metrics.search).toBeLessThanOrEqual(672);
});

test("phone viewport keeps list and composer usable without horizontal overflow", async ({ page }) => {
  await page.goto("/?mock=1");
  await expect(page.getByRole("button", { name: /Mobile companion/ })).toBeVisible();

  const targetMetrics = await page.evaluate(() => ({
    filters: [...document.querySelectorAll(".filter-button")].map((button) => button.getBoundingClientRect().height),
    delegatedSummary: document.querySelector(".delegated-disclosure > summary").getBoundingClientRect().height,
    statusline: document.querySelector(".statusline").getBoundingClientRect().height,
    statuslineFont: Number.parseFloat(getComputedStyle(document.querySelector(".statusline")).fontSize),
  }));
  expect(Math.min(...targetMetrics.filters)).toBeGreaterThanOrEqual(44);
  expect(targetMetrics.delegatedSummary).toBeGreaterThanOrEqual(44);
  expect(targetMetrics.statusline).toBeGreaterThanOrEqual(22);
  expect(targetMetrics.statuslineFont).toBeGreaterThanOrEqual(10);

  const listMetrics = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
    viewportWidth: innerWidth,
  }));
  expect(listMetrics.clientWidth).toBe(listMetrics.viewportWidth);
  expect(listMetrics.scrollWidth).toBeLessThanOrEqual(listMetrics.clientWidth);

  await page.getByRole("button", { name: /Mobile companion/ }).click();
  const workSummary = page.getByRole("button", { name: /Current work: Failed preview check/ });
  await expect(workSummary).toBeVisible();
  await expect(workSummary).toContainText("Choose whether to continue without the preview");
  await expect(workSummary).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator("#work-panel")).toBeHidden();
  await expect(page.locator("#timeline-scroll")).toBeVisible();
  const compactWorkMetrics = await page.evaluate(() => {
    const region = document.querySelector("#work-region").getBoundingClientRect();
    const summary = document.querySelector("#work-summary").getBoundingClientRect();
    return {
      left: region.left,
      right: region.right,
      bottom: region.bottom,
      summaryHeight: summary.height,
      viewportWidth: innerWidth,
      viewportHeight: innerHeight,
    };
  });
  expect(compactWorkMetrics.left).toBeGreaterThanOrEqual(0);
  expect(compactWorkMetrics.right).toBeLessThanOrEqual(compactWorkMetrics.viewportWidth);
  expect(compactWorkMetrics.bottom).toBeLessThanOrEqual(compactWorkMetrics.viewportHeight);
  expect(compactWorkMetrics.summaryHeight).toBeGreaterThanOrEqual(44);
  expect(compactWorkMetrics.summaryHeight).toBeLessThanOrEqual(64);

  await workSummary.click();
  await expect(page.getByRole("heading", { name: "Work details" })).toBeFocused();
  await expect(page.locator("#work-panel")).toBeVisible();
  await expect(page.locator("#timeline-scroll")).toBeHidden();
  await expect(page.locator("#feedback-form")).toBeHidden();
  const expandedWorkMetrics = await page.evaluate(() => {
    const region = document.querySelector("#work-region").getBoundingClientRect();
    return {
      bottom: region.bottom,
      viewportHeight: innerHeight,
      summaryTargets: [...document.querySelectorAll("#work-summary, .work-item-summary, #work-close")].map((control) => control.getBoundingClientRect().height),
    };
  });
  expect(expandedWorkMetrics.bottom).toBeLessThanOrEqual(expandedWorkMetrics.viewportHeight);
  expect(Math.min(...expandedWorkMetrics.summaryTargets)).toBeGreaterThanOrEqual(44);
  await page.locator('.work-item[data-depth="0"] > details > summary').first().click();
  await expect(page.getByText("Accessibility reviewer", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Close work details" }).click();
  await expect(workSummary).toBeFocused();
  await expect(page.locator("#timeline-scroll")).toBeVisible();
  await page.getByRole("button", { name: "Back to agents" }).click();

  await page.getByRole("button", { name: /Security reviewer/ }).click();
  await expect(page.getByRole("heading", { name: "Follow the work" })).toBeHidden();
  const composer = page.getByRole("textbox", { name: "Send feedback" });
  await expect(composer).toBeVisible();
  await composer.fill("Mobile feedback draft");
  await expect(page.getByRole("button", { name: "Send feedback" })).toBeInViewport();

  const detailMetrics = await page.evaluate(() => {
    const composer = document.querySelector(".composer").getBoundingClientRect();
    const row = document.querySelector(".composer-row").getBoundingClientRect();
    const input = document.querySelector("#feedback-input").getBoundingClientRect();
    const toolbar = document.querySelector(".composer-toolbar").getBoundingClientRect();
    const send = document.querySelector("#send-feedback").getBoundingClientRect();
    const controls = [...document.querySelectorAll(".composer-toolbar button:not([hidden])")].map((button) => button.getBoundingClientRect().height);
    return {
      clientWidth: document.documentElement.clientWidth,
      scrollWidth: document.documentElement.scrollWidth,
      composerHeight: composer.height,
      inputWidth: input.width,
      inputHeight: input.height,
      inputRight: input.right,
      toolbarLeft: toolbar.left,
      sendRight: send.right,
      rowRight: row.right,
      controls,
    };
  });
  expect(detailMetrics.scrollWidth).toBeLessThanOrEqual(detailMetrics.clientWidth);
  expect(detailMetrics.inputWidth).toBeGreaterThanOrEqual(100);
  expect(detailMetrics.inputHeight).toBeGreaterThanOrEqual(44);
  expect(detailMetrics.inputHeight).toBeLessThanOrEqual(72);
  expect(detailMetrics.inputRight).toBeLessThanOrEqual(detailMetrics.toolbarLeft + 1);
  expect(detailMetrics.sendRight).toBeLessThanOrEqual(detailMetrics.rowRight);
  expect(Math.min(...detailMetrics.controls)).toBeGreaterThanOrEqual(44);
  expect(detailMetrics.composerHeight).toBeLessThanOrEqual(72);
});
