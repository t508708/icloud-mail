import assert from "node:assert/strict";
import test from "node:test";
import { mergePoolPageSelection, poolAliasSelectable, submitPoolEnrollment } from "../src/utils/poolEnrollment.js";

test("page selection preserves other pages and supports unchecking current rows", () => {
  const first = [{ id: 1 }, { id: 2 }];
  const second = [{ id: 51 }, { id: 52 }];
  let selected = mergePoolPageSelection([], first, first);
  selected = mergePoolPageSelection(selected, second, second);
  assert.deepEqual(selected, [1, 2, 51, 52]);
  selected = mergePoolPageSelection(selected, first, [{ id: 2 }]);
  assert.deepEqual(selected, [51, 52, 2]);
  assert.deepEqual(mergePoolPageSelection(selected, second, []), [2]);
});

test("enrollment excludes inactive accounts, old credentials and unconfirmed aliases", () => {
  const alias = { id: 1, accountId: 4, enabled: true, credentialMode: "v2", credentialVersion: 1 };
  const accounts = new Set([4]);
  assert.equal(poolAliasSelectable(alias, accounts), true);
  for (const patch of [
    { accountId: 5 }, { enabled: false }, { credentialMode: "legacy" },
    { credentialVersion: 0 }, { lastSyncError: "APPLE_ALIAS_CONFIRMATION_PENDING" },
  ]) assert.equal(poolAliasSelectable({ ...alias, ...patch }, accounts), false);
});

test("large selections are submitted in bounded batches without truncation", async () => {
  const ids = Array.from({ length: 2107 }, (_, i) => i + 1);
  const requests = [], progress = [];
  await submitPoolEnrollment([...ids, 1], async batch => requests.push(batch), step => progress.push(step));
  assert.deepEqual(requests.map(batch => batch.length), [1000, 1000, 107]);
  assert.deepEqual(requests.flat(), ids);
  assert.deepEqual(progress.map(step => step.done), [1000, 2000, 2107]);
  assert.deepEqual(progress.at(-1).remaining, []);
});

test("a failed batch retains all remaining selections and stops further requests", async () => {
  const ids = Array.from({ length: 2107 }, (_, i) => i + 1);
  let requests = 0, remaining = ids;
  await assert.rejects(submitPoolEnrollment(ids, async () => {
    if (++requests === 2) throw new Error("temporary failure");
  }, step => { remaining = step.remaining; }), /temporary failure/);
  assert.equal(requests, 2);
  assert.deepEqual(remaining, ids.slice(1000));
});

test("leaving an enrollment session prevents stale progress and follow-up batches", async () => {
  let current = true, requests = 0, progress = 0;
  await submitPoolEnrollment(Array.from({ length: 1100 }, (_, i) => i + 1), async () => {
    ++requests; current = false;
  }, () => { ++progress; }, () => current);
  assert.equal(requests, 1);
  assert.equal(progress, 0);
});
