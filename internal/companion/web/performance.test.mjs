import test from "node:test";
import assert from "node:assert/strict";
import { createPerformanceTracker } from "./performance.mjs";

test("performance tracker records local operation durations", async () => {
  const times = [10, 14.26];
  const tracker = createPerformanceTracker({ performance: { now: () => times.shift() }, PerformanceObserver: null });
  const value = await tracker.measure("bootstrap", async () => "done");
  assert.equal(value, "done");
  assert.deepEqual(tracker.snapshot(), {
    samples: [{ name: "bootstrap", duration: 4.3 }],
    vitals: { longTasks: 0, longTaskMilliseconds: 0, layoutShift: 0 },
  });
});

test("performance tracker bounds samples", () => {
  const tracker = createPerformanceTracker({ PerformanceObserver: null });
  for (let index = 0; index < 50; index += 1) tracker.add(`sample-${index}`, index);
  const samples = tracker.snapshot().samples;
  assert.equal(samples.length, 40);
  assert.equal(samples[0].name, "sample-10");
});
