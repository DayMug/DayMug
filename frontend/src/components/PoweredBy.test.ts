import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import PoweredBy from "./PoweredBy.vue";
import { PROJECT_URL } from "@/lib/project";

describe("PoweredBy", () => {
  it("links the attribution to the project repository in a new tab", () => {
    const wrapper = mount(PoweredBy);
    expect(wrapper.text()).toContain("Powered by DayMug");
    const link = wrapper.get("a");
    expect(link.attributes("href")).toBe(PROJECT_URL);
    expect(link.attributes("target")).toBe("_blank");
    expect(link.attributes("rel")).toContain("noopener");
  });
});
