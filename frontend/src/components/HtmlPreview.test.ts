import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";

import HtmlPreview from "./HtmlPreview.vue";

// The component subscribes through useFileWatch; this stub captures the
// callbacks so a test can fire a change without a socket, and exposes the
// live path list it is asking the server to watch.
const watchHooks = vi.hoisted(() => ({
  onChanged: null as ((path: string) => void) | null,
  paths: null as { value: string[] } | null,
}));
vi.mock("@/composables/useFileWatch", () => ({
  useFileWatch: (opts: { paths: { value: string[] }; onChanged: (p: string) => void }) => {
    watchHooks.onChanged = opts.onChanged;
    watchHooks.paths = opts.paths;
    return { mode: { value: "ws" }, stop: () => {} };
  },
}));

const PREFIX = "http://localhost:3000/api/users/u1/files/preview/";

vi.mock("@/composables/useFileApi", async () => {
  const actual = await vi.importActual<typeof import("@/composables/useFileApi")>(
    "@/composables/useFileApi",
  );
  return {
    ...actual,
    useFileApi: () => ({ previewFileUrl: (_uid: string, path: string) => `${PREFIX}${path}` }),
    previewUrlPrefix: () => PREFIX,
    workspacePathFromPreviewUrl: (_uid: string, url: string) =>
      url.startsWith(PREFIX) ? url.slice(PREFIX.length) : null,
  };
});

interface FakeFrameWindow {
  performance: { getEntriesByType: (type: string) => { name: string }[] };
  location: { reload: () => void };
}

// happy-dom never loads a real document into the frame, so the test supplies
// the contentWindow the component reads back — the resource timeline it
// samples and the reload it calls.
function stubFrameWindow(
  el: HTMLIFrameElement,
  resources: string[],
): FakeFrameWindow & { reloads: number } {
  const win = {
    reloads: 0,
    performance: { getEntriesByType: () => resources.map((name) => ({ name })) },
    location: {
      reload() {
        win.reloads++;
      },
    },
  };
  Object.defineProperty(el, "contentWindow", { value: win, configurable: true });
  return win;
}

// happy-dom never navigates the frame, so a test that cares about the framed
// document supplies one of its own.
function stubFrameDocument(el: HTMLIFrameElement): Document {
  const doc = document.implementation.createHTMLDocument("preview");
  Object.defineProperty(el, "contentDocument", { value: doc, configurable: true });
  return doc;
}

function mountPreview(path = "site/index.html") {
  return mount(HtmlPreview, { props: { userId: "u1", path } });
}

describe("HtmlPreview", () => {
  beforeEach(() => {
    vi.useRealTimers();
    watchHooks.onChanged = null;
    watchHooks.paths = null;
  });

  it("frames the file through the path-shaped preview endpoint", () => {
    const frame = mountPreview().find("iframe");
    expect(frame.attributes("src")).toBe(`${PREFIX}site/index.html`);
    // Without allow-same-origin the page loses localStorage and every
    // fetch-based load, wasm included.
    expect(frame.attributes("sandbox")).toContain("allow-same-origin");
    expect(frame.attributes("sandbox")).toContain("allow-scripts");
  });

  it("defaults the previewed page's links to a new tab", async () => {
    const wrapper = mountPreview();
    const frame = wrapper.find("iframe");
    const doc = stubFrameDocument(frame.element as HTMLIFrameElement);

    await frame.trigger("load");

    // Without this a link replaces the report the user is reading, and the
    // sandbox leaves no way back to it.
    expect(doc.head.querySelector("base")?.getAttribute("target")).toBe("_blank");
  });

  it("leaves a page that states its own base target alone", async () => {
    const wrapper = mountPreview();
    const frame = wrapper.find("iframe");
    const doc = stubFrameDocument(frame.element as HTMLIFrameElement);
    const own = doc.createElement("base");
    own.setAttribute("target", "_self");
    doc.head.append(own);

    await frame.trigger("load");

    expect(doc.head.querySelectorAll("base")).toHaveLength(1);
    expect(doc.head.querySelector("base")?.getAttribute("target")).toBe("_self");
  });

  it("watches the document plus the assets the page actually loaded", async () => {
    const wrapper = mountPreview();
    const frame = wrapper.find("iframe");
    stubFrameWindow(frame.element as HTMLIFrameElement, [
      `${PREFIX}site/app.js`,
      `${PREFIX}site/assets/app.wasm`,
      "https://cdn.example.com/lib.js",
    ]);

    expect(watchHooks.paths?.value).toEqual(["site/index.html"]);
    await frame.trigger("load");

    expect(watchHooks.paths?.value).toEqual([
      "site/index.html",
      "site/app.js",
      "site/assets/app.wasm",
    ]);
  });

  it("reloads the framed page when a watched asset changes on disk", async () => {
    vi.useFakeTimers();
    const wrapper = mountPreview();
    const frame = wrapper.find("iframe");
    const win = stubFrameWindow(frame.element as HTMLIFrameElement, [`${PREFIX}site/app.js`]);
    await frame.trigger("load");

    watchHooks.onChanged?.("site/app.js");
    watchHooks.onChanged?.("site/index.html");
    expect(win.reloads).toBe(0);

    // A rebuild rewrites several files at once; that is still one reload.
    vi.runAllTimers();
    expect(win.reloads).toBe(1);
  });

  it("reloads when the host bumps the reload token", async () => {
    vi.useFakeTimers();
    const wrapper = mountPreview();
    const frame = wrapper.find("iframe");
    const win = stubFrameWindow(frame.element as HTMLIFrameElement, []);
    await frame.trigger("load");

    await wrapper.setProps({ reloadToken: 1 });
    vi.runAllTimers();
    expect(win.reloads).toBe(1);
  });

  it("drops the previous file's assets when the path changes", async () => {
    const wrapper = mountPreview();
    const frame = wrapper.find("iframe");
    stubFrameWindow(frame.element as HTMLIFrameElement, [`${PREFIX}site/app.js`]);
    await frame.trigger("load");
    expect(watchHooks.paths?.value).toContain("site/app.js");

    await wrapper.setProps({ path: "other/page.html" });
    await flushPromises();
    expect(watchHooks.paths?.value).toEqual(["other/page.html"]);
  });
});
