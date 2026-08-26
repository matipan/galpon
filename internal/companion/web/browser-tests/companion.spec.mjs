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
    if ([...document.querySelectorAll("h1")].filter(visible).length < 1) problems.push("visible view must have an h1");
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

test("mock agent list opens a desktop master-detail view and returns with keyboard focus", async ({ page }) => {
  await openMockAgentList(page);

  const row = page.getByRole("button", { name: /Mobile companion/ });
  await row.focus();
  await page.keyboard.press("Enter");

  await expect(page).toHaveURL(/#agent=agent-captain$/);
  await expect(page.getByRole("heading", { name: "Mobile companion" })).toBeVisible();
  await expect(page.getByText(/Build the phone companion without touching/)).toBeVisible();
  await expect(page.getByRole("heading", { name: "Mobile companion" })).toBeFocused();
  await expect(page.getByRole("heading", { name: "Follow the work" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Mobile companion/ })).toHaveAttribute("aria-current", "true");
  const shellMetrics = await page.evaluate(() => {
    const list = document.querySelector("#agents-screen").getBoundingClientRect();
    const detail = document.querySelector("#detail-screen").getBoundingClientRect();
    const timelineElement = document.querySelector("#timeline");
    const timeline = timelineElement.getBoundingClientRect();
    const timelineStyle = getComputedStyle(timelineElement);
    const composer = document.querySelector(".composer-row").getBoundingClientRect();
    return {
      listWidth: list.width,
      detailLeft: detail.left,
      listRight: list.right,
      discussionLeft: timeline.left + Number.parseFloat(timelineStyle.paddingLeft),
      discussionRight: timeline.right - Number.parseFloat(timelineStyle.paddingRight),
      composerLeft: composer.left,
      composerRight: composer.right,
    };
  });
  expect(shellMetrics.listWidth).toBeGreaterThanOrEqual(360);
  expect(shellMetrics.listWidth).toBeLessThanOrEqual(420);
  expect(Math.abs(shellMetrics.detailLeft - shellMetrics.listRight)).toBeLessThanOrEqual(1);
  expect(Math.abs(shellMetrics.discussionLeft - shellMetrics.composerLeft)).toBeLessThanOrEqual(2);
  expect(Math.abs(shellMetrics.discussionRight - shellMetrics.composerRight)).toBeLessThanOrEqual(2);

  await page.getByRole("button", { name: "Back to agents" }).click();
  await expect(page.getByRole("heading", { name: "Follow the work" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Follow the work" })).toBeFocused();
  await expect(page.getByRole("button", { name: /Mobile companion/ })).not.toHaveAttribute("aria-current", "true");
  await expect(page.locator("#statusline-primary")).toHaveText("4 AGENTS");
  await expect(page).not.toHaveURL(/#agent=/);
});

test("workspace operations is read-only, responsive, and keeps observed facts separate from reports", async ({ page }) => {
  await openMockAgentList(page);
  const operations = page.getByRole("button", { name: "Open read-only operations for Galpon" });
  await operations.click();

  await expect(page).toHaveURL(/#operations=workspace-galpon$/);
  await expect(page.getByRole("heading", { name: "Galpon operations" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Work outline" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Selected detail" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Agent runtime" })).toBeVisible();
  await expect(page.getByText(/Observed delivery · Started.*Lease observed/)).toBeVisible();
  await expect(page.getByText(/Observed activity · tool: read · completed ·/).first()).toBeVisible();
  await expect(page.getByText("Direct Pi work · Waiting", { exact: true })).toBeVisible();
  await expect(page.getByText(/1 direct operation · none lease · observed/)).toBeVisible();
  await expect(page.getByText(/durable inbound queued/i)).toBeVisible();
  await expect(page.getByText(/Agent report · Verifying/)).toBeVisible();
  await expect(page.getByText(/Protocol v2 · source operation waiting · result delivery ready · result receipt presented · todo settlement pending/i)).toBeVisible();
  await expect(page.getByText("Resume queued").locator("..").locator("dd")).toHaveText("1");
  await expect(page.getByText("Receipts presented").locator("..").locator("dd")).toHaveText("1");
  await expect(page.getByText("TODO work pending").locator("..").locator("dd")).toHaveText("1");
  await expect(page.getByText("Legacy suppression unknown").locator("..").locator("dd")).toHaveText("1");
  const liveMark = page.locator('.operations-work-button[data-live="true"] .operations-work-mark').first();
  await expect(liveMark).toHaveCSS("animation-name", "observed-lease-pulse");
  expect(await liveMark.evaluate((mark) => getComputedStyle(mark).animationIterationCount)).not.toBe("infinite");
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(liveMark).toHaveCSS("animation-name", "none");
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await expect(page.locator("#operations-screen")).not.toContainText("runtimeId");
  await expect(page.locator("#operations-screen")).not.toContainText("session");
  await expect(page.locator("#operations-screen")).not.toContainText("private prompt");

  const firstWork = page.locator("#operations-work-list button").first();
  await firstWork.focus();
  await page.keyboard.press("ArrowDown");
  await expect(page.getByRole("button", { name: /Accessibility reviewer, stale observation/ })).toBeFocused();
  const staleWork = page.getByRole("button", { name: /Accessibility reviewer, stale observation/ });
  await staleWork.click();
  await expect(page.getByText("This is a stale observation. It does not mean that work is stuck.", { exact: true })).toBeVisible();
  expect(await staleWork.locator(".operations-work-mark").evaluate((mark) => getComputedStyle(mark).animationName)).toBe("none");
  const columns = await page.locator(".operations-layout").evaluate((element) => getComputedStyle(element).gridTemplateColumns.split(" ").length);
  expect(columns).toBe(3);
  expect(await scanBasicAccessibility(page)).toEqual([]);

  await page.getByRole("button", { name: /Failed preview check/ }).click();
  await expect(page.getByText(/Durable result delivery queued; Pi handling is not observed/)).toBeVisible();

  await page.setViewportSize({ width: 390, height: 780 });
  expect(await page.locator(".operations-layout").evaluate((element) => getComputedStyle(element).gridTemplateColumns.split(" ").length)).toBe(1);
  await expect(page.getByRole("heading", { name: "Work outline" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Selected detail" })).toBeHidden();
  await staleWork.click();
  await expect(page.getByRole("heading", { name: "Selected detail" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Work outline" })).toBeHidden();
  const detailBack = page.getByRole("button", { name: "Back to work outline" });
  await expect(detailBack).toBeFocused();
  await detailBack.click();
  await expect(staleWork).toBeFocused();
  await expect(page.locator("#agents-screen")).toBeHidden();
  await page.getByRole("button", { name: "Back to agents" }).click();
  await expect(page.getByRole("heading", { name: "Follow the work" })).toBeFocused();
});

test("initial operations failure focuses its heading and Retry recovers", async ({ page }) => {
  await page.goto("/?mock=1&operationsFailOnce=1");
  await expect(page.getByRole("heading", { name: "Follow the work" })).toBeVisible();
  await page.getByRole("button", { name: "Open read-only operations for Galpon" }).click();
  const failure = page.getByRole("heading", { name: "Operations unavailable" });
  await expect(failure).toBeVisible();
  await expect(failure).toBeFocused();
  await page.getByRole("button", { name: "Retry" }).click();
  await expect(page.getByRole("heading", { name: "Galpon operations" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Operations unavailable" })).toBeHidden();
});

test("current work is compact, accessible, expandable, responsive, and privacy safe", async ({ page }) => {
  await openMockAgentList(page);
  await page.getByRole("button", { name: /Mobile companion/ }).click();

  const workSummary = page.getByRole("button", { name: /Current work: Failed preview check/ });
  await expect(workSummary).toBeVisible();
  await expect(workSummary).toHaveAttribute("aria-expanded", "false");
  await expect(workSummary).toContainText("Needs input · Choose whether to continue without the preview");
  await expect(page.getByText("2 active · 1 need you", { exact: true })).toBeVisible();
  await expect(page.locator("#work-panel")).toBeHidden();
  await expect(page.locator("#timeline-scroll")).toBeVisible();
  await expect(page.locator("#feedback-form")).toBeVisible();
  await expect(page.locator("#work-region")).not.toContainText("runtime");
  await expect(page.locator("#work-region")).not.toContainText("session");
  await expect(page.locator("#work-region")).not.toContainText("/");

  await workSummary.focus();
  await expect(page.locator("#work-peek")).toBeVisible();
  await page.locator("#detail-title").focus();
  await expect(page.locator("#work-peek")).toBeHidden();
  await workSummary.hover();
  await expect(page.locator("#work-peek")).toBeVisible();
  await expect(page.locator("#work-peek")).toContainText("Updated");
  await page.mouse.move(0, 0);
  await expect(page.locator("#work-peek")).toBeHidden();

  await workSummary.press("Enter");
  await expect(workSummary).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByRole("heading", { name: "Work details" })).toBeFocused();
  await expect(page.locator("#work-panel")).toBeVisible();
  await expect(page.locator("#timeline-scroll")).toBeHidden();
  await expect(page.locator("#feedback-form")).toBeHidden();
  await expect(page.getByText("Choose whether to continue without the preview", { exact: true })).toBeVisible();
  await expect(page.getByText("Preview approval", { exact: true })).toBeVisible();

  const parent = page.locator('.work-item[data-depth="0"] > details').first();
  await expect(parent).not.toHaveAttribute("open", "");
  await parent.locator(":scope > summary").click();
  await expect(parent).toHaveAttribute("open", "");
  await expect(page.getByText("Accessibility reviewer", { exact: true })).toBeVisible();
  await expect(page.getByText("Historical report · Waiting for a product choice", { exact: true })).toBeVisible();
  await page.getByText("Accessibility reviewer", { exact: true }).click();
  await expect(page.getByText(/Historical agent report · Waiting for a product choice/)).toBeVisible();
  await expect(page.getByText(/This can be a delayed update/)).toBeVisible();
  await expect(page.getByText(/Last activity: responding · started ·/)).toBeVisible();
  await expect(page.getByRole("progressbar", { name: "browser checks: 7 of 12" })).toHaveAttribute("value", "7");
  await expect(page.locator("#work-scroll-cue")).toHaveText("Scroll for more work");
  await page.locator("#work-list-frame").evaluate((frame) => {
    frame.scrollTop = frame.scrollHeight;
    frame.dispatchEvent(new Event("scroll"));
  });
  await expect(page.locator("#work-scroll-cue")).toHaveText("End of work list");

  const expectWorkFullyInViewport = async () => {
    const bounds = await page.locator("#work-region").boundingBox();
    const viewport = page.viewportSize();
    expect(bounds.y).toBeGreaterThanOrEqual(0);
    expect(bounds.y + bounds.height).toBeLessThanOrEqual(viewport.height);
  };
  await expect(page.locator("#work-region")).toBeInViewport();
  await expectWorkFullyInViewport();
  const metrics = await page.evaluate(() => {
    const summary = document.querySelector("#work-summary").getBoundingClientRect();
    const frame = document.querySelector("#work-list-frame").getBoundingClientRect();
    const close = document.querySelector("#work-close").getBoundingClientRect();
    const rows = [...document.querySelectorAll(".work-item-summary")].map((row) => row.getBoundingClientRect().height);
    return { summaryHeight: summary.height, frameHeight: frame.height, closeHeight: close.height, rowHeights: rows, viewportHeight: innerHeight };
  });
  expect(metrics.summaryHeight).toBeGreaterThanOrEqual(44);
  expect(metrics.closeHeight).toBeGreaterThanOrEqual(44);
  expect(Math.min(...metrics.rowHeights)).toBeGreaterThanOrEqual(44);
  expect(metrics.frameHeight).toBeGreaterThan(metrics.viewportHeight * 0.4);

  await page.keyboard.press("Escape");
  await expect(workSummary).toHaveAttribute("aria-expanded", "false");
  await expect(workSummary).toBeFocused();
  await expect(page.locator("#timeline-scroll")).toBeVisible();
  await workSummary.press("Enter");

  await page.setViewportSize({ width: 800, height: 900 });
  await expect(page.locator("#work-region")).toBeInViewport();
  await expectWorkFullyInViewport();
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.locator("#work-region")).toBeInViewport();
  await expectWorkFullyInViewport();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);

  await page.getByRole("button", { name: "Close work details" }).click();
  await page.getByRole("button", { name: "Back to agents" }).click();
  await page.getByRole("button", { name: /Security reviewer/ }).click();
  await expect(page.locator("#work-region")).toBeHidden();
  await expect(page.locator("#detail-loading")).toBeVisible();
  expect(await scanBasicAccessibility(page)).toEqual([]);
});

test("work refresh preserves summary focus without restoring it after a failed agent change", async ({ page }) => {
  const agent = (id, title) => ({ id, title, role: "tester", status: "running", updatedAt: new Date().toISOString() });
  const focusAgent = agent("agent-focus", "Focus worker");
  const failAgent = agent("agent-fail", "Failing worker");
  await page.route("**/api/v1/bootstrap", (route) => route.fulfill({ json: {
    cursor: 1, audioMessages: false, repositories: [],
    workspaces: [{ id: "workspace", title: "Galpon", agents: [focusAgent, failAgent] }],
  } }));
  await page.route("**/api/v1/events?*", (route) => route.abort());
  await page.route(/\/api\/v1\/agents\/agent-focus(?:\?.*)?$/, (route) => route.fulfill({ json: {
    cursor: 2,
    agent: { ...focusAgent, workspaceId: "workspace", workspaceTitle: "Galpon" },
    timeline: [], hasMore: false,
    work: [{ id: "focus-work", title: "Stable disclosure", createdAt: Date.now(), updatedAt: Date.now(), observation: { state: "started", source: "observed", lease: "fresh" }, children: [] }],
  } }));
  await page.route("**/api/v1/agents/agent-focus/messages", (route) => route.fulfill({ json: {
    id: "feedback-message", prompt: "Refresh without moving focus", response: "", status: "queued", createdAt: Date.now(), updatedAt: Date.now(),
  } }));
  await page.route(/\/api\/v1\/agents\/agent-fail(?:\?.*)?$/, (route) => route.fulfill({ status: 503, json: { error: "Focused detail failed" } }));

  await page.goto("/");
  await page.getByRole("button", { name: /Focus worker/ }).click();
  const summary = page.getByRole("button", { name: /Current work: Stable disclosure/ });
  await summary.focus();
  await expect(summary).toBeFocused();
  await page.evaluate(() => {
    const input = document.querySelector("#feedback-input");
    input.value = "Refresh without moving focus";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector("#feedback-form").requestSubmit();
  });
  await expect(page.getByText("Refresh without moving focus", { exact: true })).toBeVisible();
  await expect(summary).toBeFocused();

  await page.getByRole("button", { name: /Failing worker/ }).click();
  await expect(page.getByText("Focused detail failed", { exact: true })).toBeVisible();
  await expect(page.locator("#work-region")).toBeHidden();
  await page.waitForTimeout(100);
  expect(await page.evaluate(() => document.activeElement?.id === "work-summary")).toBe(false);
  await expect(page.locator("#detail-title")).toBeFocused();
});

test("an empty truncated work projection explains its bounded state", async ({ page }) => {
  const agent = { id: "agent-bounded", title: "Bounded worker", role: "tester", status: "idle", updatedAt: new Date().toISOString() };
  await page.route("**/api/v1/bootstrap", (route) => route.fulfill({ json: {
    cursor: 1, audioMessages: false, repositories: [],
    workspaces: [{ id: "workspace", title: "Galpon", agents: [agent] }],
  } }));
  await page.route("**/api/v1/events?*", (route) => route.abort());
  await page.route("**/api/v1/agents/agent-bounded", (route) => route.fulfill({ json: {
    cursor: 2,
    agent: { ...agent, workspaceId: "workspace", workspaceTitle: "Galpon" },
    timeline: [], hasMore: false, work: [], workTruncated: true,
  } }));

  await page.goto("/");
  await page.getByRole("button", { name: /Bounded worker/ }).click();
  await expect(page.locator("#work-region")).toBeVisible();
  const summary = page.getByRole("button", { name: /Current work: Bounded work view/ });
  await expect(summary).toHaveAttribute(
    "aria-label", "Current work: Bounded work view. Visible work details are outside this bounded view. 0 active and recent delegated work items; more omitted. Open work details.",
  );
  await expect(page.getByText("No work items in view", { exact: true })).toBeHidden();
  await summary.click();
  await expect(page.getByText("No work items in view", { exact: true })).toBeVisible();
  await expect(page.getByText("The bounded projection has no visible item.", { exact: true })).toBeVisible();
  await expect(page.getByText("More work is outside this bounded view.", { exact: true })).toBeVisible();
});

test("desktop detail Back returns directly to the list after several selections", async ({ page }) => {
  await openMockAgentList(page);
  await page.getByRole("button", { name: /Mobile companion/ }).click();
  await page.getByRole("button", { name: /Security reviewer/ }).click();
  await expect(page).toHaveURL(/#agent=agent-reviewer$/);
  await page.getByRole("button", { name: "Back to agents" }).click();
  await expect(page).not.toHaveURL(/#agent=/);
  await expect(page.locator("#detail-screen")).toBeHidden();
  await expect(page.getByRole("heading", { name: "Follow the work" })).toBeFocused();
});

test("master-detail responds when the viewport crosses the wide breakpoint", async ({ page }) => {
  await openMockAgentList(page);
  await page.getByRole("button", { name: /Mobile companion/ }).click();
  await expect(page.locator("#agents-screen")).toBeVisible();
  await page.setViewportSize({ width: 800, height: 900 });
  await expect(page.locator("#agents-screen")).toBeHidden();
  await page.setViewportSize({ width: 1280, height: 900 });
  await expect(page.locator("#agents-screen")).toBeVisible();
});

test("live activity refresh stays stable until list navigation", async ({ page }) => {
  const makeAgent = (id, title, updatedAt, workspaceId) => ({
    id,
    title,
    role: "tester",
    status: "idle",
    updatedAt,
    workspaceId,
  });
  const initial = {
    cursor: 1,
    audioMessages: false,
    repositories: [],
    workspaces: [
      {
        id: "workspace-one",
        title: "First workspace",
        agents: [
          makeAgent("agent-a", "Agent A", 500, "workspace-one"),
          makeAgent("agent-b", "Agent B", 400, "workspace-one"),
          makeAgent("agent-c", "Agent C", 300, "workspace-one"),
        ],
      },
      {
        id: "workspace-two",
        title: "Second workspace",
        agents: [makeAgent("agent-x", "Agent X", 200, "workspace-two")],
      },
    ],
  };
  const refreshed = structuredClone(initial);
  refreshed.cursor = 2;
  refreshed.workspaces[0].agents[2].updatedAt = 900;
  refreshed.workspaces[0].agents.push(makeAgent("agent-d", "Agent D", 450, "workspace-one"));
  refreshed.workspaces[1].agents[0].updatedAt = 1_000;

  let current = initial;
  let bootstrapRequests = 0;
  let releaseEvent;
  const eventReady = new Promise((resolve) => { releaseEvent = resolve; });
  let eventRequests = 0;
  await page.route("**/api/v1/bootstrap", (route) => {
    bootstrapRequests += 1;
    return route.fulfill({ json: current });
  });
  await page.route("**/api/v1/events?*", async (route) => {
    eventRequests += 1;
    if (eventRequests > 1) return route.abort();
    await eventReady;
    current = refreshed;
    return route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body: 'id: 2\nevent: invalidate\ndata: {"seq":2}\n\n',
    });
  });
  await page.route("**/api/v1/agents/agent-b", (route) => route.fulfill({
    json: {
      cursor: 2,
      agent: {
        ...makeAgent("agent-b", "Agent B", 400, "workspace-one"),
        workspaceTitle: "First workspace",
      },
      timeline: [],
      hasMore: false,
    },
  }));

  await page.goto("/");
  await expect(page.getByRole("button", { name: /Agent B/ })).toBeVisible();
  await page.getByRole("button", { name: /Agent B/ }).focus();
  releaseEvent();
  await expect.poll(() => bootstrapRequests).toBe(2);

  await expect(page.locator(".agent-row-title")).toHaveText(["Agent A", "Agent D", "Agent B", "Agent C", "Agent X"]);
  await expect(page.locator(".agent-row-detail").first()).toContainText("First workspace");
  await expect(page.locator(".agent-row-detail").last()).toContainText("Second workspace");
  await expect(page.getByRole("button", { name: /Agent B/ })).toBeFocused();

  await page.getByRole("button", { name: /Agent B/ }).click();
  await expect(page.getByRole("heading", { name: "Agent B" })).toBeVisible();
  await page.getByRole("button", { name: "Back to agents" }).click();

  await expect(page.locator(".agent-row-title")).toHaveText(["Agent X", "Agent C", "Agent A", "Agent D", "Agent B"]);
  await expect(page.locator(".workspace-group")).toHaveCount(0);
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
        id: "agent-old-work",
        title: "Old work agent",
        role: "tester",
        status: "running",
        updatedAt: new Date().toISOString(),
      }, {
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
  await page.route("**/api/v1/agents/agent-old-work", (route) => route.fulfill({ json: {
    cursor: 1,
    agent: { id: "agent-old-work", title: "Old work agent", role: "tester", status: "running", workspaceId: "workspace", workspaceTitle: "Galpon" },
    timeline: [], hasMore: false,
    work: [{ id: "old-item", title: "Old delegated item", createdAt: Date.now(), updatedAt: Date.now(), observation: { state: "started", source: "observed", lease: "fresh" }, children: [] }],
  } }));
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
  await page.getByRole("button", { name: /Old work agent/ }).click();
  await expect(page.getByRole("button", { name: /Current work: Old delegated item/ })).toBeVisible();
  await page.getByRole("button", { name: "Back to agents" }).click();
  await page.getByRole("button", { name: /Retry agent/ }).click();
  await expect(page.locator("#work-region")).toBeHidden();
  await expect(page.locator("#work-primary-title")).not.toHaveText("Old delegated item");
  await expect(page.getByText("Temporary detail failure")).toBeVisible();
  await expect(page.getByText("Discussion unavailable")).toBeVisible();
  await expect(page.locator("#work-region")).toBeHidden();

  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Retry agent" })).toBeVisible();
  await expect(page.getByText("Temporary detail failure")).toBeHidden();
  await expect(page.getByRole("textbox", { name: "Send feedback" })).toBeEnabled();
  expect(detailRequests).toBe(2);
});

test("a delayed prior detail cannot replace the selected agent work", async ({ page }) => {
  const agent = (id, title) => ({ id, title, role: "tester", status: "running", updatedAt: new Date().toISOString() });
  await page.route("**/api/v1/bootstrap", (route) => route.fulfill({ json: {
    cursor: 1, audioMessages: false, repositories: [],
    workspaces: [{ id: "workspace", title: "Galpon", agents: [agent("agent-a", "Agent A"), agent("agent-b", "Agent B")] }],
  } }));
  await page.route("**/api/v1/events?*", (route) => route.abort());
  const detail = (id, title) => ({
    cursor: 2, agent: { id, title, role: "tester", status: "running", workspaceId: "workspace", workspaceTitle: "Galpon" }, timeline: [], hasMore: false,
    work: [{ id: `work-${id}`, title: `${title} delegated work`, createdAt: Date.now(), updatedAt: Date.now(), observation: { state: "started", source: "observed", lease: "fresh" }, children: [] }],
  });
  await page.route("**/api/v1/agents/agent-a", async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 450));
    await route.fulfill({ json: detail("agent-a", "Agent A") }).catch(() => {});
  });
  await page.route("**/api/v1/agents/agent-b", async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 40));
    await route.fulfill({ json: detail("agent-b", "Agent B") });
  });
  await page.goto("/");
  await page.getByRole("button", { name: /Agent A/ }).click();
  await page.getByRole("button", { name: /Agent B/ }).click();
  await expect(page.getByRole("heading", { name: "Agent B" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Current work: Agent B delegated work/ })).toBeVisible();
  await page.waitForTimeout(500);
  await expect(page.getByRole("heading", { name: "Agent B" })).toBeVisible();
  await expect(page.locator("#work-primary-title")).not.toHaveText("Agent A delegated work");
});

test("a delayed prior detail failure cannot clear the selected agent", async ({ page }) => {
  const agent = (id, title) => ({ id, title, role: "tester", status: "running", updatedAt: new Date().toISOString() });
  await page.route("**/api/v1/bootstrap", (route) => route.fulfill({ json: {
    cursor: 1, audioMessages: false, repositories: [],
    workspaces: [{ id: "workspace", title: "Galpon", agents: [agent("agent-a", "Agent A"), agent("agent-b", "Agent B")] }],
  } }));
  await page.route("**/api/v1/events?*", (route) => route.abort());
  await page.route("**/api/v1/agents/agent-a", async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 300));
    await route.fulfill({ status: 503, json: { error: "Old detail failed" } }).catch(() => {});
  });
  await page.route("**/api/v1/agents/agent-b", (route) => route.fulfill({ json: {
    cursor: 2, agent: { id: "agent-b", title: "Agent B", role: "tester", status: "running", workspaceId: "workspace", workspaceTitle: "Galpon" },
    timeline: [], hasMore: false, work: [],
  } }));
  await page.goto("/");
  await page.getByRole("button", { name: /Agent A/ }).click();
  await page.getByRole("button", { name: /Agent B/ }).click();
  await expect(page.getByRole("heading", { name: "Agent B" })).toBeVisible();
  await page.waitForTimeout(400);
  await expect(page.getByRole("heading", { name: "Agent B" })).toBeVisible();
  await expect(page.getByText("Old detail failed")).toBeHidden();
  await expect(page.getByText("Discussion unavailable")).toBeHidden();
});

test("filters report matches against the complete agent count", async ({ page }) => {
  await openMockAgentList(page);
  await page.getByRole("button", { name: "Needs you" }).click();
  await expect(page.locator("#statusline-primary")).toHaveText("1 NEED YOU · 4 AGENTS");
  await page.getByRole("button", { name: "All" }).click();
  await page.getByRole("searchbox", { name: "Search agent and workspace titles" }).fill("Background test runner");
  await expect(page.locator("#statusline-primary")).toHaveText("1 MATCHES · 4 AGENTS");
  await page.getByRole("searchbox", { name: "Search agent and workspace titles" }).fill("no matching title");
  await expect(page.locator("#statusline-primary")).toHaveText("0 MATCHES · 4 AGENTS");
});

test("new agent launch stays disabled until all required choices are valid", async ({ page }) => {
  await openMockAgentList(page);
  await page.getByRole("button", { name: "New agent" }).click();
  const submit = page.getByRole("button", { name: "Create and start" });
  await expect(submit).toBeDisabled();
  await expect(page.locator("#new-agent-workspace")).toHaveValue("");
  await expect(page.locator("#new-agent-repository")).toHaveValue("");
  await page.locator("#new-agent-workspace").selectOption("workspace-galpon");
  await page.locator("#new-agent-repository").selectOption("repository-galpon");
  await page.getByLabel("Agent name").fill("Audit worker");
  await page.getByLabel("First task").fill("Check the responsive companion.");
  await expect(submit).toBeEnabled();
  await expect(page.locator("#launch-summary")).toContainText("Workspace: Galpon · Repository: Galpon · Private worktree");
  await expect(submit).toHaveCSS("opacity", "1");
});

test("failed bootstrap has one detailed in-place failure presentation", async ({ page }) => {
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
  await expect(page.getByText("Temporary bootstrap failure", { exact: true })).toHaveCount(1);
  await expect(page.locator("#network-banner")).toBeHidden();
  await expect(page.getByRole("button", { name: "Retry connection" })).toBeVisible();
  await page.getByRole("button", { name: "Retry connection" }).click();
  await expect(page.getByRole("button", { name: "Retry connection" })).toBeVisible();
  await page.getByRole("button", { name: "Retry connection" }).click();
  await expect(page.getByText("Your galpón is quiet")).toBeVisible();
  await expect(page.getByRole("button", { name: "Retry connection" })).toBeHidden();
  expect(bootstrapRequests).toBe(3);
});

test("a refreshed prompt anchor does not split or shrink its tool group", async ({ page }) => {
  const agent = {
    id: "agent-anchor",
    title: "Anchor worker",
    role: "tester",
    status: "running",
    workspaceId: "workspace",
    workspaceTitle: "Galpon",
  };
  const event = (seq, kind, values = {}) => ({
    seq,
    eventId: `event-${seq}`,
    kind,
    createdAt: `2026-08-20T12:00:${String(seq).padStart(2, "0")}Z`,
    ...values,
  });
  const prompt = event(1, "delivery_delivered", {
    eventId: "delivery:active:prompt",
    role: "user",
    content: "Inspect the active turn",
  });
  const tool = (seq, name, phase) => event(seq, `tool_execution_${phase}`, {
    role: "tool",
    toolName: "read",
    toolCallId: name,
    state: phase === "start" ? "running" : "completed",
  });
  const initial = {
    cursor: 6,
    agent,
    timeline: [prompt, tool(2, "a", "start"), tool(3, "a", "end"), tool(4, "b", "start"), tool(5, "b", "end"), tool(6, "c", "start")],
    hasMore: true,
    conversationHasMore: true,
    catchupAfter: 6,
    before: 1,
  };
  const futurePrompt = event(100, "delivery_delivered", {
    eventId: "delivery:future:prompt",
    role: "user",
    content: "Queued after the current catch-up range",
    isAnchor: true,
  });
  const refreshed = {
    cursor: 9,
    agent,
    timeline: [tool(7, "c", "end"), tool(8, "d", "start"), tool(9, "d", "end"), futurePrompt],
    hasMore: false,
    conversationHasMore: false,
    catchupHasMore: true,
    catchupAfter: 9,
    before: 0,
  };
  const caughtUp = {
    cursor: 11,
    agent,
    timeline: [tool(10, "e", "start"), tool(11, "e", "end")],
    hasMore: false,
    conversationHasMore: false,
    catchupHasMore: false,
    catchupAfter: 11,
    before: 0,
  };
  let detailRequests = 0;
  const detailURLs = [];
  let releaseEvent;
  const eventReady = new Promise((resolve) => { releaseEvent = resolve; });
  let eventRequests = 0;
  await page.route("**/api/v1/bootstrap", (route) => route.fulfill({
    json: {
      cursor: 1,
      audioMessages: false,
      repositories: [],
      workspaces: [{ id: "workspace", title: "Galpon", agents: [agent] }],
    },
  }));
  await page.route("**/api/v1/agents/agent-anchor*", (route) => {
    detailRequests += 1;
    detailURLs.push(route.request().url());
    const response = detailRequests === 1 ? initial : detailRequests === 2 ? refreshed : caughtUp;
    return route.fulfill({ json: response });
  });
  await page.route("**/api/v1/events?*", async (route) => {
    eventRequests += 1;
    if (eventRequests > 1) return route.abort();
    await eventReady;
    return route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body: 'id: 10\nevent: invalidate\ndata: {"seq":10,"agentId":"agent-anchor","workspaceId":"workspace"}\n\n',
    });
  });

  await page.goto("/");
  await page.getByRole("button", { name: /Anchor worker/ }).click();
  await expect(page.locator('.tool-stack[aria-label="3 tool actions"]')).toBeVisible();
  releaseEvent();
  await expect.poll(() => detailRequests).toBe(3);
  expect(detailURLs[1]).toContain("?after=6");
  expect(detailURLs[2]).toContain("?after=9");

  await expect(page.locator('#timeline > .timeline-item[data-role="tools"]')).toHaveCount(1);
  await expect(page.locator('.tool-stack[aria-label="5 tool actions"]')).toBeVisible();
  await expect(page.locator("#timeline > .timeline-item")).toHaveCount(3);
  await expect(page.locator("#timeline > .timeline-item").first()).toHaveAttribute("data-role", "user");
  await expect(page.locator("#timeline > .timeline-item").last()).toHaveAttribute("data-role", "user");
});

test("long delegated groups, tool bands, code, and tables expose scroll cues", async ({ page }) => {
  const children = Array.from({ length: 7 }, (_, index) => ({
    id: `child-${index}`,
    title: `Delegated worker ${index + 1}`,
    role: "worker",
    status: "running",
    workspaceId: "workspace",
    workspaceTitle: "Audit workspace",
    updatedAt: new Date().toISOString(),
  }));
  const agent = {
    id: "agent-long",
    title: "Long content audit",
    role: "reviewer",
    status: "running",
    workspaceId: "workspace",
    workspaceTitle: "Audit workspace",
    updatedAt: new Date().toISOString(),
    delegatedAgents: children,
  };
  const timeline = [
    { seq: 1, eventId: "start", kind: "assistant_message_start", role: "assistant", createdAt: new Date().toISOString() },
    {
      seq: 2,
      eventId: "text",
      kind: "assistant_text_delta",
      role: "assistant",
      isDelta: true,
      content: `\`\`\`javascript\n${"const veryLongValue = '" + "x".repeat(240) + "';"}\n\`\`\`\n\n| ${Array.from({ length: 8 }, (_, index) => `Long column ${index + 1}`).join(" | ")} |\n| ${Array.from({ length: 8 }, () => "---").join(" | ")} |\n| ${Array.from({ length: 8 }, () => "A long table value").join(" | ")} |`,
      createdAt: new Date().toISOString(),
    },
    { seq: 3, eventId: "end", kind: "assistant_message_end", role: "assistant", state: "completed", createdAt: new Date().toISOString() },
  ];
  for (let index = 0; index < 12; index += 1) {
    timeline.push({ seq: 4 + index * 2, eventId: `tool-start-${index}`, kind: "tool_execution_start", role: "tool", toolName: "read", toolCallId: `tool-${index}`, content: `{\"path\":\"file-${index}\"}`, createdAt: new Date().toISOString() });
    timeline.push({ seq: 5 + index * 2, eventId: `tool-end-${index}`, kind: "tool_execution_end", role: "tool", toolName: "read", toolCallId: `tool-${index}`, content: "done", state: "completed", createdAt: new Date().toISOString() });
  }
  await page.route("**/api/v1/bootstrap", (route) => route.fulfill({ json: { cursor: 30, audioMessages: false, repositories: [], workspaces: [{ id: "workspace", title: "Audit workspace", agents: [agent] }] } }));
  await page.route("**/api/v1/events?*", (route) => route.abort());
  await page.route("**/api/v1/agents/agent-long", (route) => route.fulfill({ json: { cursor: 30, agent, delegatedAgents: children, timeline, hasMore: false } }));

  await page.goto("/");
  const disclosure = page.locator("details.delegated-disclosure");
  await expect(disclosure.locator("summary")).toHaveText("7 delegated agents");
  await disclosure.locator("summary").click();
  const delegated = page.getByRole("region", { name: "7 agents delegated by Long content audit" });
  await expect(delegated).toHaveAttribute("tabindex", "0");
  const delegatedRowHeights = await delegated.locator(":scope > .delegated-agent-list > li > .agent-row").evaluateAll((rows) => rows.map((row) => row.getBoundingClientRect().height));
  expect(Math.min(...delegatedRowHeights)).toBeGreaterThanOrEqual(44);
  expect(Math.max(...delegatedRowHeights)).toBeLessThanOrEqual(48);
  await expect(page.getByText("7 total · Scroll to view all")).toBeVisible();
  await page.getByRole("button", { name: /Long content audit/ }).click();

  const tools = page.getByRole("region", { name: "12 tool actions" });
  await expect(tools).toHaveAttribute("tabindex", "0");
  await expect(page.getByText("Showing 10 of 12 actions · Scroll for more")).toBeVisible();
  const code = page.getByRole("region", { name: "Scrollable javascript code block" });
  await expect(code).toHaveAttribute("tabindex", "0");
  await code.focus();
  await expect(code).toBeFocused();
  const table = page.getByRole("region", { name: "Scrollable table" });
  const tableFrame = table.locator("xpath=..");
  await expect(tableFrame).toHaveAttribute("data-overflow", "true");
  await expect(tableFrame).toHaveAttribute("data-at-start", "true");
  await expect(tableFrame).toHaveAttribute("data-at-end", "false");
  await expect(page.getByText("Swipe or scroll to see all columns")).toBeVisible();
  await table.evaluate((element) => {
    element.scrollLeft = element.scrollWidth;
    element.dispatchEvent(new Event("scroll"));
  });
  await expect(tableFrame).toHaveAttribute("data-at-start", "false");
  await expect(tableFrame).toHaveAttribute("data-at-end", "true");

  await page.evaluate(async () => {
    const code = document.querySelector(".discussion-text pre");
    const frame = document.querySelector(".discussion-table-frame");
    code.removeAttribute("tabindex");
    code.removeAttribute("role");
    code.removeAttribute("aria-label");
    frame.removeAttribute("data-overflow");
    const { refreshRichTextOverflow } = await import("/rich-text.mjs");
    refreshRichTextOverflow(document.querySelector("#timeline"));
  });
  await expect(code).toHaveAttribute("tabindex", "0");
  await expect(tableFrame).toHaveAttribute("data-overflow", "true");
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

test("assistant Markdown renders headings, tables, quotes, tasks, and emphasis", async ({ page }) => {
  await openMockAgentList(page);
  await page.getByRole("button", { name: /Security reviewer/ }).click();

  await expect(page.getByRole("heading", { name: "Security summary", level: 3 })).toBeVisible();
  const table = page.getByRole("table");
  await expect(table.getByRole("columnheader", { name: "Boundary" })).toBeVisible();
  await expect(table.getByRole("cell", { name: "Browser API" })).toBeVisible();
  await expect(page.locator("blockquote")).toContainText("Keep raw HTML escaped");
  await expect(page.getByRole("checkbox", { name: "Completed task" })).toBeChecked();
  await expect(page.getByText("sound", { exact: true })).toHaveCSS("font-weight", /^(700|750|760|bold)$/);
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
