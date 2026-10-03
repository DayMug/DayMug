import { describe, expect, it, vi } from "vitest";
import { ref } from "vue";

import { useArtifactPanel, type ArtifactWorkbenchHandle } from "./useArtifactPanel";

function setup(workbench?: Partial<ArtifactWorkbenchHandle>) {
  const onFullscreenChange = vi.fn();
  const onCollapse = vi.fn();
  const handle = ref<ArtifactWorkbenchHandle | null>(null);
  const panel = useArtifactPanel({ workbench: handle, onFullscreenChange, onCollapse });
  if (workbench) {
    handle.value = {
      requestNavigateAway: vi.fn(async () => true),
      requestClosePath: vi.fn(async () => true),
      ...workbench,
    };
  }
  return { panel, onFullscreenChange, onCollapse };
}

describe("useArtifactPanel", () => {
  it("opens each file once as a tab and leaves the directory view", async () => {
    const { panel } = setup();
    await panel.open("a.md");
    await panel.open("b.csv");
    await panel.open("a.md");

    expect(panel.paths.value).toEqual(["a.md", "b.csv"]);
    expect(panel.activePath.value).toBe("a.md");
    expect(panel.directoryOpen.value).toBe(false);
  });

  it("leaves edit mode whenever another tab takes over", async () => {
    const { panel } = setup();
    await panel.open("a.md");
    panel.editMode.value = true;
    await panel.open("b.md");
    expect(panel.editMode.value).toBe(false);

    panel.editMode.value = true;
    await panel.activate("a.md");
    expect(panel.editMode.value).toBe(false);
  });

  it("stays on the active tab when the workbench refuses to leave it", async () => {
    const { panel } = setup({ requestNavigateAway: vi.fn(async () => false) });
    await panel.open("a.md");
    await panel.open("b.md");
    await panel.activate("b.md");

    expect(panel.paths.value).toEqual(["a.md"]);
    expect(panel.activePath.value).toBe("a.md");
  });

  it("does not consult the workbench when re-activating the active tab", async () => {
    const requestNavigateAway = vi.fn(async () => false);
    const { panel } = setup({ requestNavigateAway });
    await panel.open("a.md");
    requestNavigateAway.mockClear();

    await panel.activate("a.md");
    expect(requestNavigateAway).not.toHaveBeenCalled();
  });

  it("closing the active tab falls back to its left neighbour, then its right", async () => {
    const { panel } = setup();
    await panel.open("a.md");
    await panel.open("b.md");
    await panel.open("c.md");
    await panel.activate("b.md");

    await panel.close("b.md");
    expect(panel.activePath.value).toBe("a.md");

    await panel.close("a.md");
    expect(panel.activePath.value).toBe("c.md");
    expect(panel.directoryOpen.value).toBe(false);
  });

  it("returns to the directory once the last tab closes", async () => {
    const { panel } = setup();
    await panel.open("a.md");
    await panel.close("a.md");

    expect(panel.paths.value).toEqual([]);
    expect(panel.activePath.value).toBeNull();
    expect(panel.directoryOpen.value).toBe(true);
  });

  it("closing a background tab keeps the active one", async () => {
    const { panel } = setup();
    await panel.open("a.md");
    await panel.open("b.md");

    await panel.close("a.md");
    expect(panel.paths.value).toEqual(["b.md"]);
    expect(panel.activePath.value).toBe("b.md");
  });

  it("keeps a tab whose unsaved buffer the user chose not to discard", async () => {
    const { panel } = setup({ requestClosePath: vi.fn(async () => false) });
    await panel.open("a.md");
    await panel.close("a.md");

    expect(panel.paths.value).toEqual(["a.md"]);
  });

  it("shows the directory without dropping the open tabs", async () => {
    const { panel } = setup();
    await panel.open("a.md");
    panel.showDirectory();

    expect(panel.directoryOpen.value).toBe(true);
    expect(panel.activePath.value).toBe("a.md");
  });

  it("announces fullscreen toggles", () => {
    const { panel, onFullscreenChange } = setup();
    panel.toggleFullscreen();
    panel.toggleFullscreen();
    expect(onFullscreenChange.mock.calls).toEqual([[true], [false]]);
  });

  it("leaves fullscreen before collapsing", () => {
    const { panel, onFullscreenChange, onCollapse } = setup();
    panel.collapse();
    expect(onFullscreenChange).not.toHaveBeenCalled();
    expect(onCollapse).toHaveBeenCalledTimes(1);

    panel.toggleFullscreen();
    onFullscreenChange.mockClear();
    panel.collapse();
    expect(panel.fullscreen.value).toBe(false);
    expect(onFullscreenChange).toHaveBeenCalledWith(false);
    expect(onCollapse).toHaveBeenCalledTimes(2);
  });

  it("reset drops every tab and leaves fullscreen", async () => {
    const { panel, onFullscreenChange } = setup();
    await panel.open("a.md");
    panel.editMode.value = true;
    panel.toggleFullscreen();
    onFullscreenChange.mockClear();

    panel.reset();
    expect(panel.paths.value).toEqual([]);
    expect(panel.activePath.value).toBeNull();
    expect(panel.directoryOpen.value).toBe(true);
    expect(panel.editMode.value).toBe(false);
    expect(onFullscreenChange).toHaveBeenCalledWith(false);

    panel.reset();
    expect(onFullscreenChange).toHaveBeenCalledTimes(1);
  });
});
