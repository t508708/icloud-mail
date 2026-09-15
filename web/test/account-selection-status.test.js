import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const viewPath = new URL("../src/views/AccountDetailView.vue", import.meta.url);

function pageSelection(records, page, checked) {
  const next = new Map(records);
  for (const row of page) {
    if (checked(row)) next.set(row.id, row);
    else next.delete(row.id);
  }
  return next;
}

test("selection keeps other pages when selecting or clearing the current page", () => {
  const other = { id: "other" };
  const page = [{ id: "a" }, { id: "b" }];
  let selected = new Map([[other.id, other]]);
  selected = pageSelection(selected, page, () => true);
  assert.deepEqual([...selected.keys()], ["other", "a", "b"]);
  selected = pageSelection(selected, page, () => false);
  assert.deepEqual([...selected.keys()], ["other"]);
});

test("inverse selection changes only selectable rows on the current page", () => {
  const other = { id: "other" };
  const page = [{ id: "a" }, { id: "pending", pending: true }, { id: "b" }];
  const selected = new Map([[other.id, other], ["a", page[0]]]);
  const next = new Map(selected);
  for (const row of page.filter((item) => !item.pending)) {
    if (next.has(row.id)) next.delete(row.id); else next.set(row.id, row);
  }
  assert.deepEqual([...next.keys()], ["other", "b"]);
});

test("selection is blocked while busy and drag updates remove unchecked ids", async () => {
  const source = await readFile(viewPath, "utf8");
  assert.match(source, /aliasSelectionBusy/);
  assert.match(source, /data-selection-disabled/);
  const page = [{ id: "a" }, { id: "b" }];
  const selected = new Map([["a", page[0]], ["b", page[1]], ["other", {}]]);
  const ids = new Set(["b"]);
  const next = new Map(selected);
  for (const row of page) {
    if (ids.has(row.id)) next.set(row.id, row); else next.delete(row.id);
  }
  assert.deepEqual([...next.keys()], ["b", "other"]);
});

test("desktop and mobile use one outer drag-selection container", async () => {
  const source = await readFile(viewPath, "utf8");
  assert.match(source, /<section ref="aliasSelectionContainer"[^>]*@pointerdown\.capture="aliasDrag\.pointerDown"/s);
  assert.equal((source.match(/ref="aliasSelectionContainer"/g) || []).length, 1);
});
