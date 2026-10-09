import { describe, it, expect } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import ImageLightbox from "./ImageLightbox.vue";

// Teleport puts the overlay on document.body, which the wrapper's queries
// cannot reach; stubbing it keeps the markup inline without changing behaviour.
function mountViewer() {
  return mount(ImageLightbox, {
    props: { src: "/api/users/u1/files/read?path=shot.png", alt: "shot.png" },
    global: { stubs: { teleport: true } },
  });
}

function scaleOf(wrapper: ReturnType<typeof mountViewer>): number {
  const transform = wrapper.get('[data-testid="image-lightbox-image"]').attributes("style") ?? "";
  return Number(/scale\(([\d.]+)\)/.exec(transform)?.[1] ?? NaN);
}

describe("ImageLightbox", () => {
  it("renders the image it was given", () => {
    const wrapper = mountViewer();
    const img = wrapper.get('[data-testid="image-lightbox-image"]');
    expect(img.attributes("src")).toBe("/api/users/u1/files/read?path=shot.png");
    expect(img.attributes("alt")).toBe("shot.png");
  });

  it("closes when the backdrop around the image is clicked", async () => {
    const wrapper = mountViewer();
    await wrapper.get('[data-testid="image-lightbox"]').trigger("click");
    expect(wrapper.emitted("close")).toHaveLength(1);
  });

  // The whole point of the overlay is to look at the picture — a click that
  // lands on it must not dismiss what the user is inspecting.
  it("stays open when the image itself is clicked", async () => {
    const wrapper = mountViewer();
    await wrapper.get('[data-testid="image-lightbox-image"]').trigger("click");
    expect(wrapper.emitted("close")).toBeUndefined();
  });

  it("closes on Escape and on the close button", async () => {
    const wrapper = mountViewer();
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(wrapper.emitted("close")).toHaveLength(1);
    await wrapper.get('[data-testid="image-lightbox-close"]').trigger("click");
    expect(wrapper.emitted("close")).toHaveLength(2);
  });

  it("zooms in and back out through the controls", async () => {
    const wrapper = mountViewer();
    expect(scaleOf(wrapper)).toBe(1);

    await wrapper.get('[data-testid="image-lightbox-zoom-in"]').trigger("click");
    expect(scaleOf(wrapper)).toBeGreaterThan(1);

    await wrapper.get('[data-testid="image-lightbox-reset"]').trigger("click");
    expect(scaleOf(wrapper)).toBe(1);
  });

  it("zooms with the wheel and double-click", async () => {
    const wrapper = mountViewer();
    await wrapper.get('[data-testid="image-lightbox"]').trigger("wheel", { deltaY: -400 });
    expect(scaleOf(wrapper)).toBeGreaterThan(1);

    await wrapper.get('[data-testid="image-lightbox-image"]').trigger("dblclick");
    expect(scaleOf(wrapper)).toBe(1);
  });

  it("cannot zoom below the fitted size", async () => {
    const wrapper = mountViewer();
    await wrapper.get('[data-testid="image-lightbox"]').trigger("wheel", { deltaY: 400 });
    expect(scaleOf(wrapper)).toBe(1);
    expect(
      wrapper.get('[data-testid="image-lightbox-zoom-out"]').attributes("disabled"),
    ).toBeDefined();
  });

  // Releasing a pan over empty space is a drag, not a click on the backdrop.
  // Without this guard every pan that ended outside the picture closed the
  // viewer and threw away the zoom the user had just set up.
  it("does not close when a pan gesture ends on the backdrop", async () => {
    const wrapper = mountViewer();
    const stage = wrapper.get('[data-testid="image-lightbox"]');
    await wrapper.get('[data-testid="image-lightbox-zoom-in"]').trigger("click");

    await stage.trigger("pointerdown", { pointerId: 1, clientX: 100, clientY: 100 });
    await stage.trigger("pointermove", { pointerId: 1, clientX: 180, clientY: 140 });
    await stage.trigger("pointerup", { pointerId: 1 });
    await stage.trigger("click");

    expect(wrapper.emitted("close")).toBeUndefined();
  });

  it("offers the image as a download", () => {
    const wrapper = mountViewer();
    const link = wrapper.get('[data-testid="image-lightbox-download"]');
    expect(link.attributes("href")).toBe("/api/users/u1/files/read?path=shot.png");
    expect(link.attributes("download")).toBe("shot.png");
  });

  // 100% must mean the image's own pixels, not "fitted to the window": a
  // 4000px screenshot fitted into 1000px reads 25%, and one click on it shows
  // it pixel-for-pixel.
  it("labels zoom against natural size and opens oversized images at 100% on click", async () => {
    const wrapper = mountViewer();
    const img = wrapper.get('[data-testid="image-lightbox-image"]');
    Object.defineProperty(img.element, "naturalWidth", { value: 4000 });
    Object.defineProperty(img.element, "offsetWidth", { value: 1000 });
    await img.trigger("load");

    // The Teleport stub re-renders its slot a tick later and re-creates the
    // <img>, so wait for it and re-query instead of holding the old node.
    await flushPromises();
    const current = () => wrapper.get('[data-testid="image-lightbox-image"]');
    const label = () => wrapper.get('[data-testid="image-lightbox-reset"]').text();
    expect(label()).toBe("25%");
    expect(current().classes()).toContain("cursor-zoom-in");

    await current().trigger("click");
    expect(scaleOf(wrapper)).toBe(4);
    expect(label()).toBe("100%");

    await current().trigger("click");
    expect(scaleOf(wrapper)).toBe(1);
    expect(label()).toBe("25%");
  });

  it("shows images that fit the viewport at 100% without a zoom-in cursor", async () => {
    const wrapper = mountViewer();
    const img = wrapper.get('[data-testid="image-lightbox-image"]');
    Object.defineProperty(img.element, "naturalWidth", { value: 400 });
    Object.defineProperty(img.element, "offsetWidth", { value: 400 });
    await img.trigger("load");
    await flushPromises();

    const current = () => wrapper.get('[data-testid="image-lightbox-image"]');
    expect(wrapper.get('[data-testid="image-lightbox-reset"]').text()).toBe("100%");
    expect(current().classes()).not.toContain("cursor-zoom-in");
    await current().trigger("click");
    expect(scaleOf(wrapper)).toBe(1);
  });

  it("locks body scrolling while open and restores it on close", () => {
    document.body.style.overflow = "auto";
    const wrapper = mountViewer();
    expect(document.body.style.overflow).toBe("hidden");
    wrapper.unmount();
    expect(document.body.style.overflow).toBe("auto");
  });
});
