const maximumSamples = 40;

export function createPerformanceTracker({ performance = globalThis.performance, PerformanceObserver = globalThis.PerformanceObserver } = {}) {
  const samples = [];
  const vitals = { longTasks: 0, longTaskMilliseconds: 0, layoutShift: 0 };
  const add = (name, duration) => {
    const value = Number(duration);
    if (!Number.isFinite(value) || value < 0) return;
    samples.push({ name: String(name), duration: Math.round(value * 10) / 10 });
    if (samples.length > maximumSamples) samples.splice(0, samples.length - maximumSamples);
  };
  const measure = async (name, operation) => {
    const started = performance?.now?.() ?? Date.now();
    try {
      return await operation();
    } finally {
      add(name, (performance?.now?.() ?? Date.now()) - started);
    }
  };
  const observe = (type, callback) => {
    if (!PerformanceObserver) return;
    try {
      const observer = new PerformanceObserver((list) => callback(list.getEntries()));
      observer.observe({ type, buffered: true });
    } catch {
      // The browser does not support this entry type.
    }
  };
  observe("longtask", (entries) => {
    for (const entry of entries) {
      vitals.longTasks += 1;
      vitals.longTaskMilliseconds += Number(entry.duration || 0);
    }
  });
  observe("layout-shift", (entries) => {
    for (const entry of entries) {
      if (!entry.hadRecentInput) vitals.layoutShift += Number(entry.value || 0);
    }
  });
  return {
    add,
    measure,
    snapshot: () => ({
      samples: samples.map((sample) => ({ ...sample })),
      vitals: {
        longTasks: vitals.longTasks,
        longTaskMilliseconds: Math.round(vitals.longTaskMilliseconds * 10) / 10,
        layoutShift: Math.round(vitals.layoutShift * 1_000) / 1_000,
      },
    }),
  };
}
