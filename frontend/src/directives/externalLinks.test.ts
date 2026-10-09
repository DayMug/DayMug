import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { defineComponent, ref } from "vue";
import { vExternalLinks } from "./externalLinks";

// The directive's whole job is what happens around the click, so the harness
// mirrors the real shape: a row that collapses on click, with rendered
// markdown inside it.
const Host = defineComponent({
  directives: { externalLinks: vExternalLinks },
  props: { html: { type: String, required: true } },
  setup() {
    const rowClicks = ref(0);
    return { rowClicks };
  },
  template: `
    <div class="row" @click="rowClicks++">
      <div v-external-links v-html="html"></div>
    </div>`,
});

describe("vExternalLinks", () => {
  it("opens a link in a new tab without disturbing the row it sits in", async () => {
    const wrapper = mount(Host, {
      props: { html: '<p><a href="https://example.com">docs</a></p>' },
    });

    await wrapper.get("a").trigger("click");

    const anchor = wrapper.get("a");
    expect(anchor.attributes("target")).toBe("_blank");
    expect(anchor.attributes("rel")).toBe("noopener noreferrer");
    expect(wrapper.vm.rowClicks).toBe(0);
  });

  it("leaves an in-document anchor in this tab", async () => {
    const wrapper = mount(Host, { props: { html: '<p><a href="#heading">top</a></p>' } });

    await wrapper.get("a").trigger("click");

    expect(wrapper.get("a").attributes("target")).toBeUndefined();
    // Scrolling to a heading is not a reason to swallow the row's own click.
    expect(wrapper.vm.rowClicks).toBe(1);
  });

  it("lets a click on ordinary text through to the row", async () => {
    const wrapper = mount(Host, { props: { html: "<p>plain prose</p>" } });

    await wrapper.get("p").trigger("click");

    expect(wrapper.vm.rowClicks).toBe(1);
  });
});
