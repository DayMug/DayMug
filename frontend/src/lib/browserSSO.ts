// Only the initiating webview learns the claim secret; the system browser
// receives the independent OAuth state instead.
export async function browserSSO(signal: AbortSignal): Promise<void> {
  const start = await fetch("/api/auth/oidc/desktop/start", { method: "POST", signal });
  if (!start.ok) throw new Error("Unable to start browser sign-in");
  const { auth_url, claim } = (await start.json()) as { auth_url: string; claim: string };
  const target = new URL(auth_url);
  if (!["https:", "http:"].includes(target.protocol)) {
    throw new Error("Browser sign-in requires an external identity provider");
  }
  // The shell opens foreign-origin windows externally and denies their
  // creation, so a null result does not indicate a blocked popup.
  if (window.__DAYMUG_CLIENT__?.browserLoginNavigation) {
    window.location.assign(
      `/__daymug_client__/browser-login?${new URLSearchParams({ url: target.href })}`,
    );
  } else {
    window.open(target.href, "_blank", "noopener,noreferrer");
  }
  const deadline = Date.now() + 5 * 60_000;
  while (!signal.aborted && Date.now() < deadline) {
    const response = await fetch("/api/auth/oidc/desktop/claim", {
      method: "POST",
      signal,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ claim }),
    });
    if (response.status === 204) return;
    if (response.status !== 202) {
      const body = (await response.json().catch(() => null)) as { error?: string } | null;
      throw new Error(body?.error || "Browser sign-in expired. Please try again.");
    }
    await new Promise<void>((resolve) => setTimeout(resolve, 1000));
  }
  throw new Error("Browser sign-in expired. Please try again.");
}
