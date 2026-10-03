import js from "@eslint/js";
import tseslint from "typescript-eslint";
import pluginVue from "eslint-plugin-vue";
import configPrettier from "@vue/eslint-config-prettier";

export default tseslint.config(
  // `public/casual-office/` is generated: scripts/sync-casual-office.mjs
  // stages the office editor embeds there out of node_modules.
  { ignores: ["dist", "public/casual-office"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...pluginVue.configs["flat/recommended"],
  {
    files: ["**/*.vue"],
    languageOptions: {
      parserOptions: {
        parser: tseslint.parser,
      },
      globals: {
        window: "readonly",
        document: "readonly",
        HTMLDivElement: "readonly",
        HTMLFormElement: "readonly",
        HTMLTextAreaElement: "readonly",
        KeyboardEvent: "readonly",
        WebSocket: "readonly",
        MediaQueryList: "readonly",
        MediaQueryListEvent: "readonly",
        MessageEvent: "readonly",
        MouseEvent: "readonly",
        PointerEvent: "readonly",
        WheelEvent: "readonly",
        FocusEvent: "readonly",
        Element: "readonly",
        TouchEvent: "readonly",
        HTMLElement: "readonly",
        HTMLIFrameElement: "readonly",
        HTMLImageElement: "readonly",
        HTMLInputElement: "readonly",
        Event: "readonly",
        File: "readonly",
        FileList: "readonly",
        FormData: "readonly",
        localStorage: "readonly",
        navigator: "readonly",
        URL: "readonly",
        ClipboardEvent: "readonly",
        DragEvent: "readonly",
        SVGElement: "readonly",
        SVGSVGElement: "readonly",
        SVGRectElement: "readonly",
        DataTransfer: "readonly",
        DataTransferItem: "readonly",
        AbortController: "readonly",
        ResizeObserver: "readonly",
        DOMException: "readonly",
        FileSystemEntry: "readonly",
        FileSystemFileEntry: "readonly",
        FileSystemDirectoryEntry: "readonly",
        FileSystemDirectoryReader: "readonly",
        setTimeout: "readonly",
        clearTimeout: "readonly",
        fetch: "readonly",
        __APP_VERSION__: "readonly",
      },
    },
    rules: {
      // Every v-html site in this app renders HTML we produced and sanitized
      // ourselves — markdown-it with `html: false`, highlight.js escaping, and
      // DOMPurify — never raw user input. The rule's per-element suppression
      // can't sit on the v-html line inside a multi-line tag, so we disable it
      // app-wide and keep the sanitization invariant in the render helpers.
      "vue/no-v-html": "off",
    },
  },
  {
    files: ["src/components/ui/**/*.vue"],
    rules: {
      "vue/multi-word-component-names": "off",
      "vue/require-default-prop": "off",
    },
  },
  {
    // Node-run maintenance scripts (i18n checks, git-hook helpers).
    files: ["scripts/**/*.mjs"],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module",
      globals: {
        process: "readonly",
        console: "readonly",
        Buffer: "readonly",
      },
    },
  },
  configPrettier,
);
