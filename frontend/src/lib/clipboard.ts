// Shared best-effort clipboard write. Prefer the async Clipboard API;
// fall back to a hidden textarea + execCommand("copy") for non-secure
// contexts (e.g. plain http://lan-host) where navigator.clipboard is
// undefined. Errors are swallowed on both paths — every caller treats
// copying as a convenience action, not something to surface failures
// for; the boolean lets callers that want "copied!" feedback have it.
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // fall through to the legacy path
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  let ok: boolean;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  }
  document.body.removeChild(ta);
  return ok;
}
