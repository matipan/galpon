import { expect, test } from "@playwright/test";

const mockURL = "/?mock=1";

async function openMockAgentList(page) {
  await page.goto(mockURL);
  await expect(page.getByRole("heading", { name: "Follow the work" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Mobile companion/ })).toBeVisible();
  await expect(page.getByText("Mock host", { exact: true })).toBeVisible();
}

async function scanBasicAccessibility(page) {
  return page.evaluate(() => {
    const problems = [];
    const ids = new Map();
    for (const element of document.querySelectorAll("[id]")) {
      const count = (ids.get(element.id) || 0) + 1;
      ids.set(element.id, count);
      if (count === 2) problems.push(`duplicate id: ${element.id}`);
    }

    const visible = (element) => {
      const style = getComputedStyle(element);
      return !element.hidden && style.display !== "none" && style.visibility !== "hidden"
        && (element.getClientRects().length > 0 || element === document.activeElement);
    };
    const hasName = (element) => {
      if (element.getAttribute("aria-label")?.trim()) return true;
      const labelledBy = element.getAttribute("aria-labelledby")?.trim().split(/\s+/) || [];
      if (labelledBy.some((id) => document.getElementById(id)?.textContent.trim())) return true;
      if ([...(element.labels || [])].some((label) => label.textContent.trim())) return true;
      if (element.textContent.trim()) return true;
      return Boolean(element.getAttribute("title")?.trim());
    };
    for (const element of document.querySelectorAll("a[href], button, input, select, textarea, summary")) {
      if (visible(element) && !hasName(element)) {
        problems.push(`unnamed control: ${element.tagName.toLowerCase()}#${element.id || "(no id)"}`);
      }
    }
    for (const image of document.querySelectorAll("img")) {
      if (!image.hasAttribute("alt")) problems.push("image without alt text");
    }
    if (!document.querySelector("main")) problems.push("missing main landmark");
    if ([...document.querySelectorAll("h1")].filter(visible).length !== 1) problems.push("visible view must have one h1");
    return problems;
  });
}

test("real Companion adapter serves the embedded app with browser protections", async ({ page }) => {
  const response = await page.goto(mockURL);
  expect(response.headers()["content-security-policy"]).toContain("frame-ancestors 'none'");
  expect(response.headers()["cache-control"]).toBe("no-cache");
  const manifest = await page.request.get("/manifest.webmanifest");
  expect(manifest.headers()["content-type"]).toContain("application/manifest+json");
  expect(manifest.headers()["cache-control"]).toBe("no-cache");
});

test("mock agent list opens a detail and returns with keyboard focus", async ({ page }) => {
  await openMockAgentList(page);

  const row = page.getByRole("button", { name: /Mobile companion/ });
  await row.focus();
  await page.keyboard.press("Enter");

  await expect(page).toHaveURL(/#agent=agent-captain$/);
  await expect(page.getByRole("heading", { name: "Mobile companion" })).toBeVisible();
  await expect(page.getByText(/Build the phone companion without touching/)).toBeVisible();
  await expect(page.getByRole("heading", { name: "Mobile companion" })).toBeFocused();

  await page.getByRole("button", { name: "Back to agents" }).click();
  await expect(page.getByRole("heading", { name: "Follow the work" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Follow the work" })).toBeFocused();
  await expect(page.locator("#statusline-primary")).toHaveText("4 AGENTS");
  await expect(page).not.toHaveURL(/#agent=/);
});

test("agent deliveries are identified and collapsed by default", async ({ page }) => {
  await openMockAgentList(page);
  await page.getByRole("button", { name: /Mobile companion/ }).click();

  const delivery = page.locator("details.agent-delivery");
  await expect(delivery).not.toHaveAttribute("open", "");
  await expect(delivery.getByText("Result from Companion reviewer", { exact: true })).toBeVisible();
  await expect(delivery.getByText("The browser parity review found no remaining ordering defect.", { exact: true })).toBeHidden();

  await delivery.locator("summary").click();
  await expect(delivery).toHaveAttribute("open", "");
  await expect(delivery.getByText("The browser parity review found no remaining ordering defect.", { exact: true })).toBeVisible();
});

test("a direct-linked detail Back control returns to the list", async ({ page }) => {
  await page.goto(`${mockURL}#agent=agent-reviewer`);
  await expect(page.getByRole("heading", { name: "Security reviewer" })).toBeVisible();

  await page.getByRole("button", { name: "Back to agents" }).click();
  await expect(page.getByRole("heading", { name: "Follow the work" })).toBeVisible();
  await expect(page).not.toHaveURL(/#agent=/);
});

test("draft text stays isolated by agent", async ({ page }) => {
  await openMockAgentList(page);
  await page.getByRole("button", { name: /Mobile companion/ }).click();
  const composer = page.getByRole("textbox", { name: "Send feedback" });
  await composer.fill("Captain draft");
  await page.getByRole("button", { name: "Back to agents" }).click();

  await page.getByRole("button", { name: /Security reviewer/ }).click();
  await expect(composer).toHaveValue("");
  await composer.fill("Reviewer draft");
  await page.getByRole("button", { name: "Back to agents" }).click();
  await page.getByRole("button", { name: /Mobile companion/ }).click();
  await expect(composer).toHaveValue("Captain draft");
});

test("detail request failure is recoverable by returning and retrying", async ({ page }) => {
  const bootstrap = {
    cursor: 1,
    audioMessages: false,
    repositories: [{ id: "repo", title: "Galpon" }],
    workspaces: [{
      id: "workspace",
      title: "Galpon",
      agents: [{
        id: "agent-retry",
        title: "Retry agent",
        role: "tester",
        status: "idle",
        updatedAt: new Date().toISOString(),
      }],
    }],
  };
  let detailRequests = 0;
  await page.route("**/api/v1/bootstrap", (route) => route.fulfill({ json: bootstrap }));
  await page.route("**/api/v1/events?*", (route) => route.abort());
  await page.route("**/api/v1/agents/agent-retry", (route) => {
    detailRequests += 1;
    if (detailRequests === 1) {
      return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Temporary detail failure" }) });
    }
    return route.fulfill({
      json: {
        cursor: 2,
        agent: {
          id: "agent-retry",
          title: "Retry agent",
          role: "tester",
          status: "idle",
          workspaceId: "workspace",
          workspaceTitle: "Galpon",
        },
        timeline: [],
        hasMore: false,
      },
    });
  });

  await page.goto("/");
  await page.getByRole("button", { name: /Retry agent/ }).click();
  await expect(page.getByText("Temporary detail failure")).toBeVisible();
  await expect(page.getByText("Discussion unavailable")).toBeVisible();

  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Retry agent" })).toBeVisible();
  await expect(page.getByText("Temporary detail failure")).toBeHidden();
  await expect(page.getByRole("textbox", { name: "Send feedback" })).toBeEnabled();
  expect(detailRequests).toBe(2);
});

test("failed bootstrap has an in-place retry", async ({ page }) => {
  let bootstrapRequests = 0;
  await page.route("**/api/v1/bootstrap", (route) => {
    bootstrapRequests += 1;
    if (bootstrapRequests <= 2) {
      return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Temporary bootstrap failure" }) });
    }
    return route.fulfill({
      json: { cursor: 1, audioMessages: false, repositories: [], workspaces: [] },
    });
  });
  await page.route("**/api/v1/events?*", (route) => route.abort());

  await page.goto("/");
  await expect(page.getByRole("button", { name: "Retry connection" })).toBeVisible();
  await page.getByRole("button", { name: "Retry connection" }).click();
  await expect(page.getByRole("button", { name: "Retry connection" })).toBeVisible();
  await page.getByRole("button", { name: "Retry connection" }).click();
  await expect(page.getByText("Your galpón is quiet")).toBeVisible();
  await expect(page.getByRole("button", { name: "Retry connection" })).toBeHidden();
  expect(bootstrapRequests).toBe(3);
});

test("timeline refresh keeps focus on an unchanged safe link", async ({ page }) => {
  await openMockAgentList(page);
  await page.getByRole("button", { name: /Security reviewer/ }).click();
  const link = page.getByRole("link", { name: "safety guide" });
  await expect(link).toHaveAttribute("href", "https://example.test/safety");
  await page.evaluate(() => {
    const input = document.querySelector("#feedback-input");
    input.value = "Focus-preserving update";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector('a[href="https://example.test/safety"]').focus();
    document.querySelector("#feedback-form").requestSubmit();
  });
  await expect(link).toBeFocused();
  await expect(page.getByText("Focus-preserving update", { exact: true })).toBeVisible();
});

test("an assistant markdown image uses its durable Companion attachment", async ({ page }) => {
  await openMockAgentList(page);
  await page.getByRole("button", { name: /Security reviewer/ }).click();

  const image = page.getByAltText("Companion preview");
  await expect(image).toBeVisible();
  await expect(image).toHaveAttribute("src", /^data:image\/png;base64,/);
  await expect(page.getByText("![Companion preview]", { exact: false })).toBeHidden();
  await expect(page.locator(".message-images img")).toHaveCount(0);
});

test("images can be selected, removed, and sent without text", async ({ page }) => {
  await openMockAgentList(page);
  await page.getByRole("button", { name: /Mobile companion/ }).click();
  const input = page.locator("#image-input");
  await input.setInputFiles([
    { name: "first.png", mimeType: "image/png", buffer: Buffer.from("first") },
    { name: "second.webp", mimeType: "image/webp", buffer: Buffer.from("second") },
  ]);

  await expect(page.getByAltText("Selected image: first.png")).toBeVisible();
  await page.getByRole("button", { name: "Remove first.png" }).click();
  await expect(page.getByAltText("Selected image: first.png")).toBeHidden();
  await page.getByRole("textbox", { name: "Send feedback" }).evaluate((composer) => {
    const clipboard = new DataTransfer();
    clipboard.items.add(new File(["pasted"], "pasted.gif", { type: "image/gif" }));
    composer.dispatchEvent(new ClipboardEvent("paste", { bubbles: true, cancelable: true, clipboardData: clipboard }));
  });
  await expect(page.getByAltText("Selected image: pasted.gif")).toBeVisible();
  await expect(page.getByRole("button", { name: "Send feedback" })).toBeEnabled();
  await page.getByRole("button", { name: "Send feedback" }).click();

  await expect(page.getByAltText("Attached image: second.webp")).toBeVisible();
  await expect(page.getByAltText("Attached image: pasted.gif")).toBeVisible();
  await expect(page.locator("#attachment-preview")).toBeHidden();
});

test("a sent message stays once at the tail while live updates arrive", async ({ page }) => {
  await openMockAgentList(page);
  await page.getByRole("button", { name: /Mobile companion/ }).click();
  const composer = page.getByRole("textbox", { name: "Send feedback" });
  await composer.fill("Stable timeline message");
  await page.getByRole("button", { name: "Send feedback" }).click();

  const message = page.getByText("Stable timeline message", { exact: true });
  await expect(message).toHaveCount(1);
  await expect(page.getByText("I received the additional feedback.", { exact: false })).toBeVisible();
  await expect(message).toHaveCount(1);
  const roles = await page.locator("#timeline > .timeline-item").evaluateAll((rows) => rows.map((row) => row.dataset.role));
  expect(roles.at(-2)).toBe("user");
  expect(roles.at(-1)).toBe("assistant");
});

test("delayed microphone access cannot move to another agent", async ({ page }) => {
  await page.addInitScript(() => {
    let resolveMedia;
    const stream = { getTracks: () => [{ stop: () => { window.__stoppedTracks += 1; } }] };
    window.__stoppedTracks = 0;
    window.__recordersCreated = 0;
    window.__resolveMedia = () => resolveMedia(stream);
    Object.defineProperty(navigator, "mediaDevices", {
      configurable: true,
      value: { getUserMedia: () => new Promise((resolve) => { resolveMedia = resolve; }) },
    });
    window.MediaRecorder = class {
      static isTypeSupported() { return true; }
      constructor() { window.__recordersCreated += 1; }
    };
  });

  await openMockAgentList(page);
  await page.getByRole("button", { name: /Mobile companion/ }).click();
  await expect(page.getByRole("button", { name: "Record a voice message" })).toBeVisible();
  await page.getByRole("button", { name: "Record a voice message" }).click();
  await page.getByRole("button", { name: "Back to agents" }).click();
  await page.getByRole("button", { name: /Security reviewer/ }).click();
  await page.evaluate(() => window.__resolveMedia());
  await page.waitForFunction(() => window.__stoppedTracks === 1);
  expect(await page.evaluate(() => window.__recordersCreated)).toBe(0);
  await expect(page.getByRole("textbox", { name: "Send feedback" })).toBeEnabled();
});

test("local performance capture records requests without sending telemetry", async ({ page }) => {
  await openMockAgentList(page);
  await page.getByRole("button", { name: /Security reviewer/ }).click();
  await expect(page.getByRole("heading", { name: "Security reviewer" })).toBeVisible();
  const capture = await page.evaluate(() => window.__galponCompanionPerformance());
  expect(capture.samples.map((sample) => sample.name)).toEqual(expect.arrayContaining(["bootstrap.request", "agent.request"]));
  expect(capture.vitals.longTasks).toBeGreaterThanOrEqual(0);
});

test("visible list and detail controls pass basic native accessibility checks", async ({ page }) => {
  await openMockAgentList(page);
  expect(await scanBasicAccessibility(page)).toEqual([]);

  await page.getByRole("button", { name: /Security reviewer/ }).click();
  await expect(page.getByRole("heading", { name: "Security reviewer" })).toBeVisible();
  expect(await scanBasicAccessibility(page)).toEqual([]);
});
