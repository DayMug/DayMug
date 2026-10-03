import { mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";

import RunningProgressBar from "./RunningProgressBar.vue";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("RunningProgressBar", () => {
  it("gives each instance a random animation speed", () => {
    vi.spyOn(Math, "random").mockReturnValueOnce(0).mockReturnValueOnce(1);

    const first = mount(RunningProgressBar);
    const second = mount(RunningProgressBar);

    expect(first.get(".running-progress__segment").attributes("style")).toContain(
      "animation-duration: 1s",
    );
    expect(second.get(".running-progress__segment").attributes("style")).toContain(
      "animation-duration: 1.5s",
    );
  });
});
