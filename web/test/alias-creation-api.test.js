import assert from "node:assert/strict";
import test from "node:test";

import {
  createAliasCreationJob,
  getAliasCreationJob,
  stopAliasCreationJob,
  getAppleAccountAuth,
  loginAppleAccountAuth,
  verifyAppleAccountAuth,
  deleteAppleAccountAuth,
  createAliasNow,
} from "../src/api/admin.js";

function response(data, status = 200) {
  return new Response(JSON.stringify({ data }), { status, headers: { "Content-Type": "application/json" } });
}

test("alias creation job API preserves null and normalizes job fields", async () => {
  const requests = [];
  globalThis.fetch = async (url, options) => {
    requests.push({ url, options });
    if (requests.length === 1) return response({ job: null });
    return response({ job: { target: "30", completed: "2", status: "waiting", last_error: "rate limited", next_run_at: "2026-09-12T01:02:03Z", entries: [{ alias_id: 9, address: "a@icloud.com" }] } });
  };
  assert.equal(await getAliasCreationJob("a/b", "csrf"), null);
  const job = await createAliasCreationJob("a/b", { count: 30, channel: "auto" }, "csrf");
  assert.equal(job.target, 30); assert.equal(job.completed, 2); assert.equal(job.entries[0].alias_id, 9);
  assert.match(requests[1].url, /accounts\/a%2Fb\/aliases\/creation-job$/);
  assert.equal(requests[1].options.headers.get("X-CSRF-Token"), "csrf");
});

test("single alias probe sends csrf and channel once", async () => {
  let calls = 0;
  globalThis.fetch = async (url, options) => { calls += 1; assert.match(url, /create-now$/); assert.deepEqual(JSON.parse(options.body), { channel: "apple_account" }); assert.equal(options.headers.get("X-CSRF-Token"), "csrf"); return response({ alias: { id: 1, address: "a@icloud.com" } }, 201); };
  const alias = await createAliasNow("a/b", "csrf", "apple_account");
  assert.equal(alias.id, 1); assert.equal(calls, 1);
});

test("Apple Account API sends credentials and normalizes session result", async () => {
  const seen = [];
  globalThis.fetch = async (url, options) => {
    seen.push({ url, options });
    if (options.method === "GET") return response({ apple_session: { status: "authenticated", apple_id: "owner@icloud.com", region: "global" } });
    if (url.endsWith("/verify")) return response({ status: "authenticated", apple_session: { apple_id: "owner@icloud.com", region: "cn" } });
    return response({ status: "verification_required", challenge_id: "challenge-1", apple_session: { apple_id: "owner@icloud.com", region: "global" } });
  };
  const id = "x/y";
  assert.equal((await getAppleAccountAuth(id, "csrf")).status, "authenticated");
  const login = await loginAppleAccountAuth(id, { apple_id: "owner@icloud.com", password: "secret", region: "global" }, "csrf");
  assert.equal(login.challengeId, "challenge-1");
  assert.equal((await verifyAppleAccountAuth(id, { challenge_id: "challenge-1", code: "123456" }, "csrf")).status, "authenticated");
  await deleteAppleAccountAuth(id, "csrf");
  assert.match(seen[0].url, /accounts\/x%2Fy\/apple-account-auth$/);
  assert.equal(JSON.parse(seen[1].options.body).password, "secret");
  assert.equal(seen[3].options.method, "DELETE");
});
