import { afterEach, describe, expect, it } from "vitest";
import "./index.css";
import indexCss from "./index.css?inline";

afterEach(() => {
  document.body.replaceChildren();
});

describe("global typography", () => {
  it("enables antialiased font rendering", () => {
    expect(indexCss).toContain("-webkit-font-smoothing: antialiased");
    expect(indexCss).toContain("-moz-osx-font-smoothing: grayscale");
  });
});

describe("cursor surfaces", () => {
  it("keeps enabled semantic controls and their descendants on the pointer cursor", () => {
    document.body.innerHTML = `
      <button id="button"><svg><path id="button-icon" /></svg></button>
      <a href="/chat"><span id="link-label">Chat</span></a>
      <div role="button"><span id="role-label">Open</span></div>
      <div class="cursor-pointer"><svg><path id="class-icon" /></svg></div>
      <select id="select"><option>Model</option></select>
      <button id="disabled" disabled><span id="disabled-label">Wait</span></button>
    `;

    for (const selector of [
      "#button",
      "#button-icon",
      "#link-label",
      "#role-label",
      "#class-icon",
      "#select",
    ]) {
      expect(getComputedStyle(document.querySelector(selector)!).cursor, selector).toBe("pointer");
    }
    expect(getComputedStyle(document.querySelector("#disabled-label")!).cursor).not.toBe("pointer");
  });

  it("keeps pointer descendants stable while preserving editable text cursors", () => {
    document.body.innerHTML = `
      <div data-cursor-surface="pointer">
        <span id="label">Conversation</span>
        <svg><path id="icon" /></svg>
        <input id="rename" />
      </div>
    `;

    expect(getComputedStyle(document.querySelector("#label")!).cursor).toBe("pointer");
    expect(getComputedStyle(document.querySelector("#icon")!).cursor).toBe("pointer");
    expect(getComputedStyle(document.querySelector("#rename")!).cursor).toBe("text");
  });

  it("keeps composer padding on the text cursor", () => {
    document.body.innerHTML = '<div id="composer" data-cursor-surface="text"></div>';
    expect(getComputedStyle(document.querySelector("#composer")!).cursor).toBe("text");
  });
});

describe("touch-device baseline", () => {
  it("floors form-control text at 16px on coarse pointers", () => {
    // Below 16px iOS Safari zooms the page in on focus and never zooms back
    // out, so one tap on the composer leaves the whole app magnified. The rule
    // has to sit outside @layer so it outranks Tailwind's `text-sm` utility,
    // and has to key on pointer type so an iPad in landscape is covered too.
    const rule = /@media \(pointer: coarse\)\s*\{[\s\S]*?font-size: max\(16px,/;
    expect(rule.test(indexCss)).toBe(true);
    expect(indexCss).not.toMatch(/@layer base \{[^}]*@media \(pointer: coarse\)/);
  });

  it("raises a line-number gutter with the field it counts", () => {
    // Floor the textarea without floating the gutter and the numbers stop
    // pointing at their own rows, which is worse than the zoom it prevents.
    const block = /@media \(pointer: coarse\)\s*\{([\s\S]*?)\n\}/.exec(indexCss)?.[1] ?? "";
    expect(block).toContain("[data-line-gutter]");
  });

  it("stops the browser from repainting its own tap and text-size behaviour over ours", () => {
    expect(indexCss).toContain("-webkit-tap-highlight-color: transparent");
    expect(indexCss).toContain("text-size-adjust: 100%");
  });

  it("honours a reduced-motion preference without freezing transitions outright", () => {
    expect(indexCss).toContain("@media (prefers-reduced-motion: reduce)");
    expect(indexCss).toContain("transition-duration: 0.01ms !important");
  });
});
