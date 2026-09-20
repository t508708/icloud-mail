import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import vm from "node:vm";
import { transform } from "esbuild";

import { useAuth } from "../src/stores/auth.js";
import { consumeSessionBootstrap } from "../src/utils/sessionBootstrap.js";

function jsonResponse(status, payload) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function fixture(value, type = "application/json", raw = false) {
  const node = { type, textContent: raw ? value : JSON.stringify(value), removed: false, remove() { this.removed = true; } };
  const document = { getElementById: (id) => id === "icloud-admin-session" && !node.removed ? node : null };
  return { node, document };
}

const valid = {
  admin: { username: "admin" },
  csrf_token: "csrf-session-value",
  expires_at: "2030-01-01T00:00:00Z",
};

function installBootstrap(value = valid) {
  const { document } = fixture(value);
  globalThis.document = document;
}

test("auth store accepts bootstrap without fetching and rejects stale bootstrap", async () => {
  const originalDocument = globalThis.document;
  const originalFetch = globalThis.fetch;
  const performanceDescriptor = Object.getOwnPropertyDescriptor(globalThis, "performance");
  try {
    installBootstrap();
    useAuth(); // Load Vue's runtime before replacing the document with a minimal fixture.
    const auth = (await import(`../src/stores/auth.js?bootstrap=${Math.random()}`)).useAuth();
    globalThis.fetch = async () => { throw new Error("unexpected fetch"); };
    assert.equal(await auth.ensureSession(), true);
    assert.equal(auth.state.username, "admin");

    for (const [value, age] of [[null, 0], [valid, 30001]]) {
      const { document } = fixture(value);
      globalThis.document = document;
      globalThis.performance = { now: () => age };
      const isolated = (await import(`../src/stores/auth.js?fallback=${Math.random()}`)).useAuth();
      globalThis.fetch = async () => jsonResponse(200, { data: {
        admin: { username: "api-user" }, csrf_token: "api-csrf", expires_at: valid.expires_at,
      } });
      assert.equal(await isolated.ensureSession(), true);
      assert.equal(isolated.state.username, "api-user");
    }
  } finally {
    globalThis.document = originalDocument;
    if (performanceDescriptor) Object.defineProperty(globalThis, "performance", performanceDescriptor);
    else delete globalThis.performance;
    globalThis.fetch = originalFetch;
  }
});

test("force, clear, and logout discard bootstrap; concurrent checks share a request", async () => {
  const originalDocument = globalThis.document;
  const originalFetch = globalThis.fetch;
  try {
    useAuth();
    installBootstrap();
    const auth = (await import(`../src/stores/auth.js?force=${Math.random()}`)).useAuth();
    let requests = 0;
    globalThis.fetch = async (url) => {
      requests += 1;
      if (String(url).endsWith("/auth/logout")) return jsonResponse(200, { data: {} });
      await new Promise((resolve) => setTimeout(resolve, 5));
      return jsonResponse(200, { data: {
        admin: { username: "api-user" }, csrf_token: "api-csrf", expires_at: valid.expires_at,
      } });
    };
    assert.deepEqual(await Promise.all([auth.ensureSession({ force: true }), auth.ensureSession()]), [true, true]);
    assert.equal(requests, 1);
    await auth.logout();
    globalThis.document = fixture(valid).document;
    assert.equal(await auth.ensureSession(), true);
    assert.equal(auth.state.username, "api-user");

    auth.clearSession({ checked: false });
    globalThis.document = fixture(valid).document;
    await auth.ensureSession();
    assert.equal(auth.state.username, "api-user");
  } finally {
    globalThis.document = originalDocument;
    globalThis.fetch = originalFetch;
  }
});

test("consumes a valid bootstrap node once and removes it", () => {
  const { document, node } = fixture(valid);
  assert.deepEqual(consumeSessionBootstrap(document, Date.parse("2029-01-01T00:00:00Z"), 100), {
    username: "admin", csrfToken: "csrf-session-value", expiresAt: valid.expires_at,
  });
  assert.equal(node.removed, true);
  assert.equal(consumeSessionBootstrap(document, Date.parse("2029-01-01T00:00:00Z"), 100), null);
});

test("invalid, expired, and stale-navigation bootstrap values are ignored", () => {
  for (const [value, type, now, age, raw = false] of [
    [null, "application/json", 0, 0],
    ["{bad json", "application/json", 0, 0, true],
    [valid, "text/plain", 0, 0],
    [{ ...valid, admin: {} }, "application/json", 0, 0],
    [{ ...valid, csrf_token: " " }, "application/json", 0, 0],
    [valid, "application/json", Date.parse(valid.expires_at), 0],
    [valid, "application/json", 0, 30001],
  ]) {
    const { document, node } = fixture(value, type, raw);
    assert.equal(consumeSessionBootstrap(document, now, age), null);
    assert.equal(node.removed, true);
  }
  assert.equal(consumeSessionBootstrap(undefined, 0, 0), null);
});

test("bootstrap is rejected outside the navigation age window", () => {
  const { document } = fixture(valid);
  assert.equal(consumeSessionBootstrap(document, 0, -1), null);
  assert.equal(consumeSessionBootstrap(document, 0, Infinity), null);
});

test("production browser transform retains the Performance receiver", async () => {
  const source = await readFile(new URL("../src/utils/sessionBootstrap.js", import.meta.url), "utf8");
  const { code } = await transform(source, {
    format: "cjs", minify: true, target: ["es2020", "edge88", "firefox78", "chrome87", "safari14"],
  });
  const { document, node } = fixture(valid);
  const clock = { now() { assert.equal(this, clock); return 100; } };
  const module = { exports: {} };
  vm.runInNewContext(code, { module, document, performance: clock });
  const result = module.exports.consumeSessionBootstrap();
  assert.equal(result?.username, "admin");
  assert.equal(node.removed, true);
});
