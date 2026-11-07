// DayMug-Client (the desktop app) hosts this page in a webview and injects a
// marker before any page script runs. Everything desktop-specific keys off
// this one check, so a plain browser never renders, requests or pairs anything.
export interface DesktopClientInfo {
  // Per-launch secret shared between the page and the client's own
  // connection; it is how the server tells which client hosts this page.
  nonce: string;
  version: string;
  os: string;
  hostname: string;
}

declare global {
  interface Window {
    __DAYMUG_CLIENT__?: Partial<DesktopClientInfo>;
  }
}

export function desktopClient(): DesktopClientInfo | null {
  if (typeof window === "undefined") return null;
  const raw = window.__DAYMUG_CLIENT__;
  if (!raw || typeof raw.nonce !== "string" || raw.nonce.length < 32) return null;
  return {
    nonce: raw.nonce,
    version: typeof raw.version === "string" ? raw.version : "",
    os: typeof raw.os === "string" ? raw.os : "",
    hostname: typeof raw.hostname === "string" ? raw.hostname : "",
  };
}

const RECENT_KEY = "daymug.desktop.recentDirs";
const RECENT_MAX = 5;

export function recentDesktopDirs(): string[] {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(RECENT_KEY) ?? "[]");
    return Array.isArray(parsed)
      ? parsed.filter((d): d is string => typeof d === "string").slice(0, RECENT_MAX)
      : [];
  } catch {
    return [];
  }
}

export function rememberDesktopDir(dir: string) {
  const next = [dir, ...recentDesktopDirs().filter((d) => d !== dir)].slice(0, RECENT_MAX);
  try {
    localStorage.setItem(RECENT_KEY, JSON.stringify(next));
  } catch {
    // Storage full or disabled: the list is a convenience only.
  }
}

// Last path segment, for both POSIX and Windows paths.
export function dirLabel(path: string): string {
  const trimmed = path.replace(/[\\/]+$/, "");
  const parts = trimmed.split(/[\\/]/);
  return parts[parts.length - 1] || trimmed || path;
}
