import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import FileIcon from "./FileIcon.vue";
import { ICONS, resolveIcon } from "./fileIconMap";

describe("FileIcon", () => {
  it("renders an svg for directories", () => {
    const wrapper = mount(FileIcon, { props: { isDir: true } });
    expect(wrapper.find("svg").exists()).toBe(true);
    expect(wrapper.find(".file-icon-wrapper").exists()).toBe(true);
  });

  it("renders an svg for files", () => {
    const wrapper = mount(FileIcon, { props: { isDir: false, fileName: "test.txt" } });
    expect(wrapper.find("svg").exists()).toBe(true);
  });

  it("inherits color instead of hardcoding one", () => {
    const wrapper = mount(FileIcon, { props: { isDir: false, fileName: "main.go" } });
    expect(wrapper.find("svg").attributes("stroke")).toBe("currentColor");
  });

  it("respects size prop", () => {
    const wrapper = mount(FileIcon, { props: { isDir: true, size: 48 } });
    const style = wrapper.find(".file-icon-wrapper").attributes("style");
    expect(style).toContain("width: 48px");
    expect(style).toContain("height: 48px");
    expect(wrapper.find("svg").attributes("width")).toBe("48");
  });

  it("defaults to size 16", () => {
    const wrapper = mount(FileIcon, { props: { isDir: false } });
    const style = wrapper.find(".file-icon-wrapper").attributes("style");
    expect(style).toContain("width: 16px");
    expect(style).toContain("height: 16px");
  });

  it("thins the stroke once the glyph is scaled up", () => {
    const small = mount(FileIcon, { props: { isDir: false, fileName: "a.ts", size: 20 } });
    const large = mount(FileIcon, { props: { isDir: false, fileName: "a.ts", size: 50 } });
    expect(small.find("svg").attributes("stroke-width")).toBe("1.5");
    expect(large.find("svg").attributes("stroke-width")).toBe("1.25");
  });

  it("renders without fileName", () => {
    const wrapper = mount(FileIcon, { props: { isDir: false } });
    expect(wrapper.find("svg").exists()).toBe(true);
  });

  it("swaps to the open-folder glyph when a directory is expanded", () => {
    const closed = mount(FileIcon, { props: { isDir: true, fileName: "src" } });
    const open = mount(FileIcon, { props: { isDir: true, fileName: "src", expanded: true } });
    expect(open.find("svg").html()).not.toBe(closed.find("svg").html());
  });

  it("ignores `expanded` for files", () => {
    const plain = mount(FileIcon, { props: { isDir: false, fileName: "a.ts" } });
    const expanded = mount(FileIcon, { props: { isDir: false, fileName: "a.ts", expanded: true } });
    expect(expanded.find("svg").html()).toBe(plain.find("svg").html());
  });
});

describe("resolveIcon", () => {
  it("returns folder for directories", () => {
    expect(resolveIcon("src", true)).toBe("folder");
  });

  it("returns folder-open for expanded directories", () => {
    expect(resolveIcon("src", true, true)).toBe("folder-open");
  });

  it("returns file for unknown extensions", () => {
    expect(resolveIcon("data.xyz", false)).toBe("file");
  });

  it("returns file for extensionless names", () => {
    expect(resolveIcon("LICENSE", false)).toBe("file");
  });

  it.each([
    ["index.ts", "code"],
    ["app.js", "code"],
    ["App.vue", "code"],
    ["main.go", "code"],
    ["script.py", "code"],
    ["lib.rs", "code"],
    ["styles.css", "code"],
  ])("collapses source file %s onto the code glyph", (name, key) => {
    expect(resolveIcon(name, false)).toBe(key);
  });

  it.each([
    ["tsconfig.json", "json"],
    ["config.yaml", "config"],
    ["Cargo.toml", "config"],
    ["README.md", "text"],
    ["daymug.log", "text"],
    ["report.pdf", "text"],
    ["deploy.sh", "terminal"],
    ["init.zsh", "terminal"],
    ["schema.sql", "database"],
    ["data.csv", "sheet"],
    ["review.pptx", "presentation"],
    ["photo.png", "image"],
    ["logo.svg", "image"],
    ["clip.mp4", "video"],
    ["song.mp3", "audio"],
    ["bundle.zip", "archive"],
    ["backup.tar", "archive"],
  ])("maps %s to the %s glyph", (name, key) => {
    expect(resolveIcon(name, false)).toBe(key);
  });

  it.each([
    ["Dockerfile", "container"],
    ["docker-compose.yml", "container"],
    ["Makefile", "config"],
    [".gitignore", "git"],
    ["package.json", "package"],
    ["go.mod", "code"],
    ["pnpm-lock.yaml", "lock"],
  ])("maps special filename %s to the %s glyph", (name, key) => {
    expect(resolveIcon(name, false)).toBe(key);
  });

  it("handles case insensitivity for special names", () => {
    expect(resolveIcon("dockerfile", false)).toBe("container");
    expect(resolveIcon("makefile", false)).toBe("config");
    expect(resolveIcon("PACKAGE.JSON", false)).toBe("package");
  });

  it("prefers the special-filename match over the extension match", () => {
    // Both would otherwise resolve via their extension, to `json` and `config`.
    expect(resolveIcon("package.json", false)).toBe("package");
    expect(resolveIcon("pnpm-lock.yaml", false)).toBe("lock");
  });

  it("has a component registered for every icon key it can return", () => {
    const keys = [
      "folder",
      "folder-open",
      "code",
      "json",
      "config",
      "text",
      "terminal",
      "database",
      "sheet",
      "presentation",
      "image",
      "video",
      "audio",
      "archive",
      "container",
      "git",
      "package",
      "lock",
      "file",
    ] as const;
    for (const key of keys) expect(ICONS[key]).toBeTruthy();
    expect(Object.keys(ICONS).sort()).toEqual([...keys].sort());
  });
});
