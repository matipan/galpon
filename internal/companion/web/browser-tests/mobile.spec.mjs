import { expect, test } from "@playwright/test";

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

  await page.getByRole("button", { name: /Security reviewer/ }).click();
  await expect(page.getByRole("heading", { name: "Follow the work" })).toBeHidden();
  const composer = page.getByRole("textbox", { name: "Send feedback" });
  await expect(composer).toBeVisible();
  await composer.fill("Mobile feedback draft");
  await expect(page.getByRole("button", { name: "Send feedback" })).toBeInViewport();

  const detailMetrics = await page.evaluate(() => {
    const row = document.querySelector(".composer-row").getBoundingClientRect();
    const input = document.querySelector("#feedback-input").getBoundingClientRect();
    const toolbar = document.querySelector(".composer-toolbar").getBoundingClientRect();
    const send = document.querySelector("#send-feedback").getBoundingClientRect();
    return {
      clientWidth: document.documentElement.clientWidth,
      scrollWidth: document.documentElement.scrollWidth,
      rowWidth: row.width,
      inputWidth: input.width,
      inputHeight: input.height,
      inputBottom: input.bottom,
      toolbarTop: toolbar.top,
      sendRight: send.right,
      rowRight: row.right,
    };
  });
  expect(detailMetrics.scrollWidth).toBeLessThanOrEqual(detailMetrics.clientWidth);
  expect(detailMetrics.inputWidth).toBeGreaterThanOrEqual(detailMetrics.rowWidth - 12);
  expect(detailMetrics.inputHeight).toBeGreaterThanOrEqual(72);
  expect(detailMetrics.toolbarTop).toBeGreaterThanOrEqual(detailMetrics.inputBottom - 1);
  expect(detailMetrics.sendRight).toBeLessThanOrEqual(detailMetrics.rowRight);
});
