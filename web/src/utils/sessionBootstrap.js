export function consumeSessionBootstrap(
  document = globalThis.document,
  now = Date.now(),
  navigationAge,
) {
  try {
    // Keep the native receiver explicit; optional calls in default parameters
    // can lose their binding when transformed for older browser targets.
    if (navigationAge === undefined) {
      const clock = globalThis.performance;
      navigationAge = clock && typeof clock.now === "function" ? clock.now() : Infinity;
    }
    const node = document?.getElementById?.("icloud-admin-session");
    if (!node) return null;
    node.remove?.();

    if (node.type !== "application/json") return null;
    const value = JSON.parse(node.textContent || "");
    const username = value?.admin?.username;
    const csrfToken = value?.csrf_token;
    const expiresAt = value?.expires_at;
    const expiry = Date.parse(expiresAt);
    if (
      typeof username !== "string" || !username.trim() ||
      typeof csrfToken !== "string" || !csrfToken.trim() ||
      typeof expiresAt !== "string" || !Number.isFinite(expiry) || expiry <= now ||
      !Number.isFinite(navigationAge) || navigationAge < 0 || navigationAge > 30000
    ) return null;

    return { username, csrfToken, expiresAt };
  } catch {
    return null;
  }
}
