// CodeMirror 6 plumbing for FileTextEditor. Kept out of the component so the
// state machine there (buffers, save, conflicts) reads without a wall of
// extension wiring in front of it, and so a test can swap the view factory
// for one that fails the way a stale chunk does.
import { Compartment, EditorState, type Extension } from "@codemirror/state";
import {
  EditorView,
  crosshairCursor,
  drawSelection,
  dropCursor,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  keymap,
  lineNumbers,
  rectangularSelection,
} from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import {
  StreamLanguage,
  bracketMatching,
  defaultHighlightStyle,
  foldGutter,
  foldKeymap,
  indentOnInput,
  indentUnit,
  syntaxHighlighting,
  type StreamParser,
} from "@codemirror/language";
import { highlightSelectionMatches, searchKeymap } from "@codemirror/search";
import {
  autocompletion,
  closeBrackets,
  closeBracketsKeymap,
  completionKeymap,
} from "@codemirror/autocomplete";
import { oneDark } from "@codemirror/theme-one-dark";

export type EditorTheme = "light" | "dark";

export interface LanguageDef {
  // Shown in the footer; also the key the tests assert against.
  label: string;
  // Absent means plain text. Each loader is its own dynamic import so opening
  // a Markdown file never downloads the SQL grammar.
  load?: () => Promise<Extension>;
}

const legacy = (p: Promise<StreamParser<unknown>>) => p.then((m) => StreamLanguage.define(m));

const LANGS = {
  javascript: {
    label: "javascript",
    load: () => import("@codemirror/lang-javascript").then((m) => m.javascript({ jsx: true })),
  },
  typescript: {
    label: "typescript",
    load: () =>
      import("@codemirror/lang-javascript").then((m) =>
        m.javascript({ typescript: true, jsx: true }),
      ),
  },
  json: { label: "json", load: () => import("@codemirror/lang-json").then((m) => m.json()) },
  html: { label: "html", load: () => import("@codemirror/lang-html").then((m) => m.html()) },
  xml: { label: "xml", load: () => import("@codemirror/lang-xml").then((m) => m.xml()) },
  css: { label: "css", load: () => import("@codemirror/lang-css").then((m) => m.css()) },
  markdown: {
    label: "markdown",
    load: () => import("@codemirror/lang-markdown").then((m) => m.markdown()),
  },
  python: {
    label: "python",
    load: () => import("@codemirror/lang-python").then((m) => m.python()),
  },
  go: { label: "go", load: () => import("@codemirror/lang-go").then((m) => m.go()) },
  rust: { label: "rust", load: () => import("@codemirror/lang-rust").then((m) => m.rust()) },
  java: { label: "java", load: () => import("@codemirror/lang-java").then((m) => m.java()) },
  cpp: { label: "cpp", load: () => import("@codemirror/lang-cpp").then((m) => m.cpp()) },
  sql: { label: "sql", load: () => import("@codemirror/lang-sql").then((m) => m.sql()) },
  yaml: { label: "yaml", load: () => import("@codemirror/lang-yaml").then((m) => m.yaml()) },
  shell: {
    label: "shell",
    load: () => legacy(import("@codemirror/legacy-modes/mode/shell").then((m) => m.shell)),
  },
  ruby: {
    label: "ruby",
    load: () => legacy(import("@codemirror/legacy-modes/mode/ruby").then((m) => m.ruby)),
  },
  toml: {
    label: "toml",
    load: () => legacy(import("@codemirror/legacy-modes/mode/toml").then((m) => m.toml)),
  },
  ini: {
    label: "ini",
    load: () =>
      legacy(import("@codemirror/legacy-modes/mode/properties").then((m) => m.properties)),
  },
  dockerfile: {
    label: "dockerfile",
    load: () =>
      legacy(import("@codemirror/legacy-modes/mode/dockerfile").then((m) => m.dockerFile)),
  },
  proto: {
    label: "proto",
    load: () => legacy(import("@codemirror/legacy-modes/mode/protobuf").then((m) => m.protobuf)),
  },
} satisfies Record<string, LanguageDef>;

const PLAIN: LanguageDef = { label: "plaintext" };

const BY_EXT: Record<string, LanguageDef> = {
  ".js": LANGS.javascript,
  ".mjs": LANGS.javascript,
  ".cjs": LANGS.javascript,
  ".jsx": LANGS.javascript,
  ".ts": LANGS.typescript,
  ".mts": LANGS.typescript,
  ".cts": LANGS.typescript,
  ".tsx": LANGS.typescript,
  ".vue": LANGS.html,
  ".svelte": LANGS.html,
  ".html": LANGS.html,
  ".htm": LANGS.html,
  ".xml": LANGS.xml,
  ".svg": LANGS.xml,
  ".css": LANGS.css,
  ".scss": LANGS.css,
  ".less": LANGS.css,
  ".json": LANGS.json,
  ".jsonl": LANGS.json,
  ".md": LANGS.markdown,
  ".markdown": LANGS.markdown,
  ".mdx": LANGS.markdown,
  ".py": LANGS.python,
  ".go": LANGS.go,
  ".rs": LANGS.rust,
  ".java": LANGS.java,
  ".c": LANGS.cpp,
  ".h": LANGS.cpp,
  ".cpp": LANGS.cpp,
  ".hpp": LANGS.cpp,
  ".sql": LANGS.sql,
  ".yaml": LANGS.yaml,
  ".yml": LANGS.yaml,
  ".sh": LANGS.shell,
  ".bash": LANGS.shell,
  ".zsh": LANGS.shell,
  ".env": LANGS.shell,
  ".rb": LANGS.ruby,
  ".toml": LANGS.toml,
  ".ini": LANGS.ini,
  ".editorconfig": LANGS.ini,
  ".proto": LANGS.proto,
  ".dockerfile": LANGS.dockerfile,
};

// Unknown types fall back to plain text rather than guessing wrongly: an
// editable file without colours is fine, a mis-highlighted one is noise.
export function detectLanguage(path: string): LanguageDef {
  const base = (path.split("/").pop() ?? "").toLowerCase();
  if (base === "dockerfile") return LANGS.dockerfile;
  const dot = base.lastIndexOf(".");
  if (dot < 0) return PLAIN;
  return BY_EXT[base.slice(dot)] ?? PLAIN;
}

// Per-buffer settings live in compartments so a preference toggle, or a
// grammar that finishes downloading after the buffer is up, reconfigures the
// state instead of rebuilding it — rebuilding would drop the undo history.
export const compartments = {
  language: new Compartment(),
  theme: new Compartment(),
  wrap: new Compartment(),
  fontSize: new Compartment(),
};

export function themeExtension(theme: EditorTheme): Extension {
  return theme === "dark"
    ? oneDark
    : [syntaxHighlighting(defaultHighlightStyle, { fallback: true }), lightChrome];
}

export function wrapExtension(on: boolean): Extension {
  return on ? EditorView.lineWrapping : [];
}

export function fontSizeExtension(px: number): Extension {
  return EditorView.theme({ "&": { fontSize: `${px}px` } });
}

const lightChrome = EditorView.theme({
  "&": { backgroundColor: "var(--background)", color: "var(--foreground)" },
  ".cm-gutters": {
    backgroundColor: "var(--muted)",
    color: "var(--muted-foreground)",
    borderRight: "1px solid var(--border)",
  },
});

const baseTheme = EditorView.theme({
  "&": { height: "100%" },
  // The default blue focus ring clashes with the app chrome; the active-line
  // highlight already says where the caret is.
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": {
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
    lineHeight: "1.6",
  },
});

export interface BufferOptions {
  doc: string;
  theme: EditorTheme;
  wordWrap: boolean;
  fontSize: number;
  // The component-owned hooks: Mod-S and the change/cursor listener. Passed
  // in rather than imported so this module stays free of component state.
  onSave: () => void;
  onUpdate: Extension;
}

export function createBufferState(opts: BufferOptions): EditorState {
  return EditorState.create({
    doc: opts.doc,
    extensions: [
      lineNumbers(),
      highlightActiveLineGutter(),
      highlightSpecialChars(),
      history(),
      foldGutter(),
      drawSelection(),
      dropCursor(),
      EditorState.allowMultipleSelections.of(true),
      indentOnInput(),
      bracketMatching(),
      closeBrackets(),
      autocompletion(),
      rectangularSelection(),
      crosshairCursor(),
      highlightActiveLine(),
      highlightSelectionMatches(),
      EditorState.tabSize.of(2),
      indentUnit.of("  "),
      keymap.of([
        // Ahead of the defaults so the browser's "save page" never fires
        // while the editor has focus.
        {
          key: "Mod-s",
          preventDefault: true,
          run: () => {
            opts.onSave();
            return true;
          },
        },
        ...closeBracketsKeymap,
        ...defaultKeymap,
        ...searchKeymap,
        ...historyKeymap,
        ...foldKeymap,
        ...completionKeymap,
        indentWithTab,
      ]),
      baseTheme,
      compartments.language.of([]),
      compartments.theme.of(themeExtension(opts.theme)),
      compartments.wrap.of(wrapExtension(opts.wordWrap)),
      compartments.fontSize.of(fontSizeExtension(opts.fontSize)),
      opts.onUpdate,
    ],
  });
}

// What the view shows with no file mounted. Not editable: keystrokes here
// would belong to no buffer and silently go nowhere.
export function createEmptyState(): EditorState {
  return EditorState.create({ extensions: [baseTheme, EditorView.editable.of(false)] });
}

export function createEditorView(parent: HTMLElement): EditorView {
  return new EditorView({ parent });
}

// Pretty-prints JSON in place. Returns null when the text isn't valid JSON so
// the caller can leave the buffer untouched instead of clobbering it.
export function formatJSON(text: string): string | null {
  try {
    return JSON.stringify(JSON.parse(text), null, 2) + (text.endsWith("\n") ? "\n" : "");
  } catch {
    return null;
  }
}
