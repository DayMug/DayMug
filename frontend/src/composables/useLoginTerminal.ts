import { watch } from "vue";
import { Terminal, type ITheme } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { useTheme } from "./useTheme";

const DARK_THEME: ITheme = {
  background: "#0b0e14",
  foreground: "#c9d1d9",
  cursor: "#c9d1d9",
  selectionBackground: "#264f78",
  selectionForeground: "#ffffff",
  selectionInactiveBackground: "#264f78",
};
const LIGHT_THEME: ITheme = {
  background: "#ffffff",
  foreground: "#1f2328",
  cursor: "#1f2328",
  selectionBackground: "#add6ff",
  selectionForeground: "#000000",
  selectionInactiveBackground: "#add6ff",
};

// Handlers the host component wires up at mount: input forwards a keystroke to
// the PTY; resize reports the fitted terminal size so the server can relay the
// child's TUI out.
export interface LoginTerminalHandlers {
  onInput: (data: string) => void;
  onResize: (rows: number, cols: number) => void;
}

// useLoginTerminal owns an xterm.js instance used to render the raw byte stream
// of an interactive CLI (the admin login flow and the admin browser terminal).
// These CLIs draw a full TUI — cursor moves, line clears, repaints — so the only
// faithful way to display their output is a real terminal emulator. Piping the
// raw bytes into a <pre> (the old approach) surfaced the control sequences as
// visible garbage.
//
// It is a fully interactive terminal: every keystroke — including typed text and
// commands like `/login` — is forwarded to the child's stdin. Every guard around
// xterm calls is defensive so a headless test environment (no canvas) can mount
// the host component without xterm throwing.
export function useLoginTerminal() {
  const { isDark } = useTheme();
  const term = new Terminal({
    fontSize: 12,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
    cursorBlink: false,
    convertEol: false,
    theme: isDark.value ? DARK_THEME : LIGHT_THEME,
  });
  const fit = new FitAddon();
  term.loadAddon(fit);

  watch(isDark, (dark) => {
    term.options.theme = dark ? DARK_THEME : LIGHT_THEME;
  });

  let handlers: LoginTerminalHandlers | null = null;
  let opened = false;

  function mount(el: HTMLElement, h: LoginTerminalHandlers) {
    handlers = h;
    try {
      term.open(el);
      fit.fit();
      opened = true;
    } catch {
      // No canvas (e.g. happy-dom test env): skip rendering. write/fit/resize
      // below all become harmless no-ops on the un-opened terminal.
      return;
    }
    term.onData((d) => handlers?.onInput(d));
    handlers.onResize(term.rows, term.cols);
  }

  function write(data: string) {
    try {
      term.write(data);
    } catch {
      // ignore — an un-opened terminal still buffers, a disposed one is gone.
    }
  }

  // reset clears the screen + scrollback for a fresh login attempt so a prior
  // run's output doesn't linger above the new one.
  function reset() {
    try {
      term.reset();
    } catch {
      /* ignore */
    }
  }

  function refit() {
    if (!opened) return;
    try {
      fit.fit();
    } catch {
      return;
    }
    handlers?.onResize(term.rows, term.cols);
  }

  // size returns the current terminal dimensions, falling back to the standard
  // 80×24 before the terminal has been opened/fitted so the first `start` frame
  // always carries a sane window size.
  function size(): { rows: number; cols: number } {
    return { rows: term.rows || 24, cols: term.cols || 80 };
  }

  function dispose() {
    try {
      term.dispose();
    } catch {
      /* ignore */
    }
  }

  return { term, mount, write, reset, refit, size, dispose };
}
