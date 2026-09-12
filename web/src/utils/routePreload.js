// Fetch only the destination view chunk, never mailbox data or credentials.
export function preloadRoute(route) {
  if (globalThis.navigator?.connection?.saveData) return;
  for (const record of route.matched || []) {
    const component = record.components?.default;
    if (typeof component === "function") void Promise.resolve().then(component).catch(() => {});
  }
}
