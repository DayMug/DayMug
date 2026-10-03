import { computed, nextTick, onBeforeUnmount, ref } from "vue";
import { useI18n } from "vue-i18n";
import { apiBase } from "./apiBase";
import { useLoginTerminal } from "./useLoginTerminal";
import { errorMessage } from "@/lib/errorMessage";

// Flat phase machine: every transition is driven by a concrete server message
// or a local UI action.
export type AdminTerminalPhase = "idle" | "connecting" | "running" | "exited" | "error";

export interface AdminTerminalStart {
  account: string;
  // Child cwd; empty inherits the server's.
  workDir?: string;
  // Spawn the account's login flow (`claude auth login` / `codex login
  // --device-auth`) instead of the bare interactive CLI.
  login?: boolean;
}

function terminalURL(): string {
  const base = apiBase();
  if (base === "") {
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${window.location.host}/api/admin/terminal/ws`;
  }
  const u = new URL(base);
  u.protocol = u.protocol === "https:" ? "wss:" : "ws:";
  u.pathname = "/api/admin/terminal/ws";
  return u.toString();
}

// useAdminTerminalSession drives one interactive CLI process over the admin
// terminal WebSocket and renders it in xterm.js. Shared by the terminal page
// and the models page's per-account login, so both speak the protocol the same
// way. Closing the socket tears the server-side process down.
export function useAdminTerminalSession() {
  const { t } = useI18n();
  const phase = ref<AdminTerminalPhase>("idle");
  const statusMsg = ref("");
  const lastError = ref("");
  const exitCode = ref<number | null>(null);
  const current = ref<AdminTerminalStart | null>(null);

  // Once the CLI is spawned the terminal takes over the viewport; it stays up
  // on exit so the final output remains readable until the admin closes it.
  const fullscreen = computed(() => phase.value === "running" || phase.value === "exited");

  const loginTerm = useLoginTerminal();
  let host: HTMLElement | null = null;
  let termMounted = false;
  // Refits on every real size change of the host: a one-shot fit can run
  // before the overlay's layout settles and leave the bottom rows clipped.
  let resizeObserver: ResizeObserver | null = null;
  let ws: WebSocket | null = null;

  function send(msg: Record<string, unknown>) {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
  }

  function attachHost(el: HTMLElement | null) {
    host = el;
  }

  // Waits a tick so the phase change has un-hidden the overlay: xterm fitted
  // into a display:none box would size itself to zero.
  async function ensureTerminalMounted() {
    if (termMounted) return;
    await nextTick();
    if (termMounted || !host) return;
    loginTerm.mount(host, {
      onInput: (data) => send({ type: "input", data }),
      onResize: (rows, cols) => send({ type: "resize", rows, cols }),
    });
    loginTerm.term.focus();
    termMounted = true;
    resizeObserver = new ResizeObserver(() => loginTerm.refit());
    resizeObserver.observe(host);
  }

  function handleServerMessage(raw: unknown) {
    if (typeof raw !== "string") return;
    let msg: Record<string, unknown>;
    try {
      msg = JSON.parse(raw) as Record<string, unknown>;
    } catch {
      return;
    }
    switch (String(msg.type ?? "")) {
      case "started":
        phase.value = "running";
        statusMsg.value = t("settings.adminTerminal.running");
        void ensureTerminalMounted();
        break;
      case "output":
        loginTerm.write(String(msg.data ?? ""));
        void ensureTerminalMounted();
        if (phase.value === "connecting") {
          phase.value = "running";
          statusMsg.value = t("settings.adminTerminal.running");
        }
        break;
      case "exited":
        phase.value = "exited";
        exitCode.value = typeof msg.exit_code === "number" ? msg.exit_code : null;
        statusMsg.value =
          exitCode.value === 0
            ? t("settings.adminTerminal.exitedOK")
            : t("settings.adminTerminal.exitedFail", { code: exitCode.value ?? "?" });
        break;
      case "error":
        lastError.value = String(msg.message ?? "");
        if (phase.value !== "running") phase.value = "error";
        break;
    }
  }

  function start(opts: AdminTerminalStart) {
    if (!opts.account) {
      lastError.value = t("settings.adminTerminal.errors.pickAccount");
      return;
    }
    if (ws) return;

    current.value = opts;
    loginTerm.reset();
    lastError.value = "";
    exitCode.value = null;
    statusMsg.value = t("settings.adminTerminal.connecting");
    phase.value = "connecting";

    try {
      ws = new WebSocket(terminalURL());
    } catch (e) {
      lastError.value = errorMessage(e);
      phase.value = "error";
      return;
    }
    ws.onopen = () => {
      statusMsg.value = t("settings.adminTerminal.starting");
      const { rows, cols } = loginTerm.size();
      send({
        type: "start",
        account: opts.account,
        work_dir: opts.workDir ?? "",
        login: Boolean(opts.login),
        rows,
        cols,
      });
    };
    ws.onmessage = (ev) => handleServerMessage(ev.data);
    ws.onerror = () => {
      if (phase.value !== "exited") {
        lastError.value = t("settings.adminTerminal.errors.wsError");
        phase.value = "error";
      }
    };
    ws.onclose = () => {
      ws = null;
      if (phase.value === "connecting" || phase.value === "running") {
        phase.value = "error";
        if (!lastError.value) lastError.value = t("settings.adminTerminal.errors.disconnected");
      }
    };
  }

  function closeSocket() {
    if (!ws) return;
    try {
      ws.close();
    } catch {
      // The socket is gone either way.
    }
    ws = null;
  }

  // Leave the overlay and stop the process (dropping the socket kills it).
  function close() {
    closeSocket();
    phase.value = "idle";
    statusMsg.value = "";
  }

  function onWindowResize() {
    loginTerm.refit();
  }
  window.addEventListener("resize", onWindowResize);

  onBeforeUnmount(() => {
    window.removeEventListener("resize", onWindowResize);
    resizeObserver?.disconnect();
    closeSocket();
    loginTerm.dispose();
  });

  return {
    phase,
    statusMsg,
    lastError,
    exitCode,
    current,
    fullscreen,
    attachHost,
    start,
    close,
  };
}

export type AdminTerminalSession = ReturnType<typeof useAdminTerminalSession>;
