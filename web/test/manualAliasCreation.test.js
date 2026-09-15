import test from "node:test";
import assert from "node:assert/strict";
import { runSerialAliasCreation } from "../src/utils/manualAliasCreation.js";

test("manual creation is sequential and reports completed addresses", async () => {
  let inFlight = 0;
  const progress = [];
  const result = await runSerialAliasCreation({
    count: 3,
    create: async (index) => {
      assert.equal(inFlight++, 0);
      await new Promise((resolve) => setImmediate(resolve));
      inFlight--;
      return { address: `manual-${index}@icloud.com` };
    },
    onCreated: (alias, completed) => progress.push([alias.address, completed]),
  });
  assert.equal(result.completed, 3);
  assert.equal(result.stopped, false);
  assert.deepEqual(progress.map((item) => item[1]), [1, 2, 3]);
});

test("stop completes an in-flight request but starts no subsequent request", async () => {
  let active = true;
  let calls = 0;
  const result = await runSerialAliasCreation({
    count: 20,
    create: async () => { calls++; active = false; return { address: "done@icloud.com" }; },
    shouldContinue: () => active,
  });
  assert.equal(calls, 1);
  assert.equal(result.completed, 1);
  assert.equal(result.stopped, true);
});

test("Apple errors stop the batch without retrying or losing previous progress", async () => {
  let calls = 0;
  const completed = [];
  const error = new Error("APPLE_RATE_LIMITED");
  await assert.rejects(runSerialAliasCreation({
    count: 20,
    create: async () => {
      if (++calls === 2) throw error;
      return { address: "done@icloud.com" };
    },
    onCreated: (alias) => completed.push(alias.address),
  }), (caught) => caught === error);
  assert.equal(calls, 2);
  assert.deepEqual(completed, ["done@icloud.com"]);
});

test("leaving the account before a batch prevents all requests", async () => {
  const result = await runSerialAliasCreation({
    count: 20,
    shouldContinue: () => false,
    create: async () => assert.fail("unexpected create"),
  });
  assert.equal(result.completed, 0);
  assert.equal(result.stopped, true);
});
