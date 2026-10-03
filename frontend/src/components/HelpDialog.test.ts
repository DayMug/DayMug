import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import HelpDialog from "./HelpDialog.vue";

describe("HelpDialog", () => {
  it("renders markdown body as HTML", () => {
    const wrapper = mount(HelpDialog, {
      props: { markdown: "# Hello\n\nSome **bold** text." },
    });
    const body = wrapper.find(".markdown-body");
    expect(body.exists()).toBe(true);
    expect(body.html()).toContain("<h1>Hello</h1>");
    expect(body.html()).toContain("<strong>bold</strong>");
  });

  it("emits close when the X button is clicked", async () => {
    const wrapper = mount(HelpDialog, { props: { markdown: "hi" } });
    const closeBtn = wrapper.find("button[aria-label='Close']");
    expect(closeBtn.exists()).toBe(true);
    await closeBtn.trigger("click");
    expect(wrapper.emitted("close")).toBeTruthy();
  });

  it("emits close when the backdrop is clicked", async () => {
    const wrapper = mount(HelpDialog, { props: { markdown: "hi" } });
    // Click the outer fixed-overlay div (the only direct .self target).
    await wrapper.trigger("click");
    expect(wrapper.emitted("close")).toBeTruthy();
  });
});
