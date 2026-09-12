export async function runSerialAliasCreation({
  count,
  create,
  shouldContinue = () => true,
  onCreated = () => {},
}) {
  const created = [];
  for (let index = 0; index < Number(count); index += 1) {
    if (!shouldContinue()) {
      return { created, completed: created.length, stopped: true };
    }
    const alias = await create(index);
    created.push(alias);
    onCreated(alias, created.length);
  }
  return { created, completed: created.length, stopped: false };
}
