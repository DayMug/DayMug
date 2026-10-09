import type { Directive, DirectiveBinding } from "vue";
import { copyText } from "@/lib/clipboard";

export interface CodeCopyLabels {
  copy: string;
  copied: string;
}

interface CodeCopyState {
  labels: CodeCopyLabels;
  onClick: (event: MouseEvent) => void;
  resetTimers: Set<ReturnType<typeof setTimeout>>;
}

const states = new WeakMap<HTMLElement, CodeCopyState>();

const copyIcon = `
  <svg viewBox="0 0 24 24" aria-hidden="true">
    <rect width="14" height="14" x="8" y="8" rx="2" ry="2"></rect>
    <path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"></path>
  </svg>`;

const checkIcon = `
  <svg viewBox="0 0 24 24" aria-hidden="true">
    <path d="m20 6-11 11-5-5"></path>
  </svg>`;

function setButtonState(button: HTMLButtonElement, copied: boolean, labels: CodeCopyLabels) {
  const label = copied ? labels.copied : labels.copy;
  button.dataset.copied = copied ? "true" : "false";
  button.setAttribute("aria-label", label);
  button.title = label;
  button.innerHTML = copied ? checkIcon : copyIcon;
}

function decorateCodeBlocks(el: HTMLElement, labels: CodeCopyLabels) {
  const blocks = el.querySelectorAll<HTMLPreElement>("pre:not([data-code-copy])");
  for (const pre of blocks) {
    if (pre.closest(".mermaid-diagram") || !pre.querySelector("code")) continue;

    const wrapper = document.createElement("div");
    wrapper.className = "code-copy-wrapper";

    const button = document.createElement("button");
    button.type = "button";
    button.className = "code-copy-button";
    setButtonState(button, false, labels);

    pre.dataset.codeCopy = "true";
    pre.before(wrapper);
    wrapper.append(pre, button);
  }
}

function syncLabels(el: HTMLElement, labels: CodeCopyLabels) {
  const buttons = el.querySelectorAll<HTMLButtonElement>(".code-copy-button");
  for (const button of buttons) {
    setButtonState(button, button.dataset.copied === "true", labels);
  }
}

function applyCodeCopy(el: HTMLElement, binding: DirectiveBinding<CodeCopyLabels>) {
  const state = states.get(el);
  if (state) {
    state.labels = binding.value;
    syncLabels(el, state.labels);
    decorateCodeBlocks(el, state.labels);
    return;
  }

  const newState: CodeCopyState = {
    labels: binding.value,
    resetTimers: new Set(),
    onClick: () => undefined,
  };

  newState.onClick = (event) => {
    const target = event.target;
    if (!(target instanceof Element)) return;
    const button = target.closest<HTMLButtonElement>(".code-copy-button");
    if (!button || !el.contains(button)) return;

    event.preventDefault();
    event.stopPropagation();

    const code = button.parentElement?.querySelector("pre > code");
    if (!code) return;

    void copyText(code.textContent ?? "").then((copied) => {
      if (!copied || !el.contains(button)) return;
      setButtonState(button, true, newState.labels);
      const timer = setTimeout(() => {
        newState.resetTimers.delete(timer);
        if (el.contains(button)) setButtonState(button, false, newState.labels);
      }, 1500);
      newState.resetTimers.add(timer);
    });
  };

  states.set(el, newState);
  el.addEventListener("click", newState.onClick);
  decorateCodeBlocks(el, newState.labels);
}

export const vCodeCopy: Directive<HTMLElement, CodeCopyLabels> = {
  mounted: applyCodeCopy,
  updated: applyCodeCopy,
  beforeUnmount(el) {
    const state = states.get(el);
    if (!state) return;
    el.removeEventListener("click", state.onClick);
    for (const timer of state.resetTimers) clearTimeout(timer);
    states.delete(el);
  },
};
