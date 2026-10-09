// Post-login redirect validation.
//
// `main.ts` parks the pre-auth location in `?redirect=<fullPath>` and the
// login page navigates there with `window.location.assign` once the session
// exists. That query parameter is attacker-controlled — anyone can hand out
// `https://host/login?redirect=//evil.com` — so it has to be proven
// same-origin before it reaches `assign()`, or the login page becomes an
// open redirect that launders phishing links through a trusted hostname.
//
// Returning a *normalised* same-origin path rather than the caller's raw
// string is deliberate: whatever comes back has already been through the URL
// parser, so no unparsed spelling of an authority can survive.

const FALLBACK = "/";

// C0 controls plus DEL. The URL parser silently removes tab/CR/LF, so
// "/\t/evil.com" would otherwise slip an authority past the prefix checks.
// eslint-disable-next-line no-control-regex
const CONTROL_CHARS = /[\u0000-\u001f\u007f]/;

export function safeRedirect(raw: unknown, origin: string = window.location.origin): string {
  if (typeof raw !== "string" || raw === "") return FALLBACK;

  // Must be site-relative. Rejects absolute URLs ("https://evil.com"), other
  // schemes ("javascript:..."), and any value with leading whitespace — the
  // URL parser strips such whitespace, so "  //evil.com" would otherwise
  // parse as an authority.
  if (!raw.startsWith("/")) return FALLBACK;

  // "//evil.com" is protocol-relative, not site-relative. Browsers also
  // normalise backslashes to forward slashes while parsing, which makes
  // "/\evil.com" and "/\/evil.com" equivalent to "//evil.com". No legitimate
  // in-app path contains a backslash, so reject it anywhere in the string.
  if (raw.startsWith("//") || raw.includes("\\")) return FALLBACK;

  if (CONTROL_CHARS.test(raw)) return FALLBACK;

  let url: URL;
  try {
    url = new URL(raw, origin);
  } catch {
    return FALLBACK;
  }

  // The authoritative check: whatever the string spelled, it has to resolve
  // back to the origin we are already on.
  if (url.origin !== origin) return FALLBACK;

  const target = `${url.pathname}${url.search}${url.hash}`;
  // Bouncing back into the login flow would just loop the user.
  if (target.startsWith("/login")) return FALLBACK;
  return target;
}
