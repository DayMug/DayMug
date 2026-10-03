import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Each test re-imports the module so the module-level ref restarts
// from a fresh readInitial(). Without vi.resetModules() the singleton
// state would leak across tests in unpredictable order.
async function freshModule() {
  vi.resetModules();
  return import("./useTimeFormat");
}

describe("useTimeFormat", () => {
  beforeEach(() => {
    localStorage.clear();
  });
  afterEach(() => {
    localStorage.clear();
  });

  it("defaults to 'system' when nothing is persisted, so existing users see no behaviour change", async () => {
    const { useTimeFormat } = await freshModule();
    const { timeFormat, timeHour12 } = useTimeFormat();
    expect(timeFormat.value).toBe("system");
    expect(timeHour12.value).toBeUndefined();
  });

  it("hydrates from localStorage so the preference survives a reload", async () => {
    localStorage.setItem("daymug.timeFormat", "24h");
    const { useTimeFormat } = await freshModule();
    const { timeFormat, timeHour12 } = useTimeFormat();
    expect(timeFormat.value).toBe("24h");
    expect(timeHour12.value).toBe(false);
  });

  it("maps each setting to the right hour12 flag for Intl.DateTimeFormat", async () => {
    const { useTimeFormat } = await freshModule();
    const { timeHour12, setTimeFormat } = useTimeFormat();
    setTimeFormat("12h");
    expect(timeHour12.value).toBe(true);
    setTimeFormat("24h");
    expect(timeHour12.value).toBe(false);
    setTimeFormat("system");
    expect(timeHour12.value).toBeUndefined();
  });

  it("persists the chosen format to localStorage on change", async () => {
    const { useTimeFormat } = await freshModule();
    const { setTimeFormat } = useTimeFormat();
    setTimeFormat("24h");
    // Watcher runs on the next microtask under Vue's default scheduling.
    await Promise.resolve();
    await Promise.resolve();
    expect(localStorage.getItem("daymug.timeFormat")).toBe("24h");
  });

  it("ignores invalid values so a typo can't poison the ref", async () => {
    const { useTimeFormat } = await freshModule();
    const { timeFormat, setTimeFormat } = useTimeFormat();
    setTimeFormat("24h");
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    setTimeFormat("bogus" as any);
    expect(timeFormat.value).toBe("24h");
  });

  it("cycles system → 12h → 24h → system so the settings button can advance with one click", async () => {
    const { useTimeFormat } = await freshModule();
    const { timeFormat, cycleTimeFormat } = useTimeFormat();
    expect(timeFormat.value).toBe("system");
    cycleTimeFormat();
    expect(timeFormat.value).toBe("12h");
    cycleTimeFormat();
    expect(timeFormat.value).toBe("24h");
    cycleTimeFormat();
    expect(timeFormat.value).toBe("system");
  });
});
