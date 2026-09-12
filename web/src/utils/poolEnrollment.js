export function poolAliasSelectable(alias, enabledAccounts) {
  return Boolean(alias?.enabled && alias.credentialMode === "v2" &&
    alias.credentialVersion > 0 && enabledAccounts.has(alias.accountId) &&
    alias.lastSyncError !== "APPLE_ALIAS_CONFIRMATION_PENDING");
}

export function mergePoolPageSelection(selected, page, checked) {
  const pageIds = new Set(page.map((alias) => alias.id));
  return [...new Set([
    ...selected.filter((id) => !pageIds.has(id)),
    ...checked.filter((alias) => pageIds.has(alias.id)).map((alias) => alias.id),
  ])];
}

export async function submitPoolEnrollment(ids, submit, onProgress, isCurrent = () => true) {
  const pending = [...new Set(ids)];
  for (let offset = 0; offset < pending.length && isCurrent(); offset += 1000) {
    const batch = pending.slice(offset, offset + 1000);
    await submit(batch);
    if (!isCurrent()) return;
    const done = offset + batch.length;
    await onProgress({ done, total: pending.length, remaining: pending.slice(done) });
  }
}
