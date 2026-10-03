import { describe, it, expect, vi, afterEach } from "vitest";
import { reactive, ref } from "vue";
import type { RouteLocationNormalizedLoaded, Router } from "vue-router";

import { useWorkspacePreview } from "./useWorkspacePreview";

function setup(initialQuery: Record<string, string> = {}) {
  const push = vi.fn();
  const resolve = vi.fn(() => ({ href: "/resolved-href" }));
  const router = { push, resolve } as unknown as Router;
  const route = reactive({ query: { ...initialQuery } }) as RouteLocationNormalizedLoaded;
  const userId = ref("u1");
  const preview = useWorkspacePreview({ userId, router, route });
  return { preview, push, resolve, route, userId };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("useWorkspacePreview", () => {
  it("computes the preview route from the current userId", () => {
    const { preview, userId } = setup();
    expect(preview.previewRoute("docs/a.txt")).toEqual({
      name: "file",
      params: { userId: "u1" },
      query: { path: "docs/a.txt", edit: "false" },
    });
    userId.value = "u2";
    expect(preview.previewRoute("docs/a.txt").params.userId).toBe("u2");
  });

  it("pushes the preview route for same-tab opens", () => {
    const { preview, push } = setup();
    preview.openPreview("docs/a.txt", "same-tab");
    expect(push).toHaveBeenCalledWith(preview.previewRoute("docs/a.txt"));
  });

  it("preserves edit mode for same-tab opens", () => {
    const { preview, push } = setup({ path: "docs/a.txt", edit: "true" });
    preview.openPreview("docs/b.txt", "same-tab");
    expect(push).toHaveBeenCalledWith(preview.previewRoute("docs/b.txt", "true"));
  });

  it("opens an html file as a page even while the page is editing", () => {
    const { preview, push } = setup({ path: "docs/a.txt", edit: "true" });
    preview.openPreview("site/index.html", "same-tab");
    expect(push).toHaveBeenCalledWith(preview.previewRoute("site/index.html", "false"));
  });

  it("opens every new-tab preview in a fresh tab", () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const { preview, resolve } = setup();
    preview.openPreview("docs/a b.txt", "new-tab");
    expect(resolve).toHaveBeenCalledWith(preview.previewRoute("docs/a b.txt"));
    expect(open).toHaveBeenCalledWith("/resolved-href", "_blank");
  });

  it("opens the editor route in a fresh tab", () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const { preview, resolve } = setup();
    preview.openEditorInNewTab("docs/a.txt");
    expect(resolve).toHaveBeenCalledWith({
      name: "file",
      params: { userId: "u1" },
      query: { path: "docs/a.txt", edit: "true" },
    });
    expect(open).toHaveBeenCalledWith("/resolved-href", "_blank");
  });

  it("backs fullscreenPreview by the ?preview= query param", () => {
    const { preview, push } = setup({ preview: "docs/a.txt", other: "x" });
    expect(preview.fullscreenPreview.value).toBe("docs/a.txt");

    preview.fullscreenPreview.value = "docs/b.txt";
    expect(push).toHaveBeenCalledWith({ query: { preview: "docs/b.txt", other: "x" } });

    preview.fullscreenPreview.value = null;
    expect(push).toHaveBeenCalledWith({ query: { other: "x" } });

    // Setting the value already in the URL must not push a redundant entry.
    push.mockClear();
    preview.fullscreenPreview.value = "docs/a.txt";
    expect(push).not.toHaveBeenCalled();
  });

  it("closes the preview on Escape and ignores Escape when no preview is open", () => {
    const opened = setup({ preview: "docs/a.txt" });
    opened.preview.handleGlobalKeydown({ key: "Escape" } as KeyboardEvent);
    expect(opened.push).toHaveBeenCalledWith({ query: {} });

    const closed = setup();
    closed.preview.handleGlobalKeydown({ key: "Escape" } as KeyboardEvent);
    expect(closed.push).not.toHaveBeenCalled();
  });
});
