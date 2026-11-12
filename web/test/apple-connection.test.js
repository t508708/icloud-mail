import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { computed, nextTick, reactive, ref, watch } from "vue";

const source = await readFile(new URL("../src/components/AppleConnectionPanel.vue", import.meta.url), "utf8");
const script = source.match(/<script setup>([\s\S]*?)<\/script>/)[1].replace(/^import[^\n]*\n/gm, "");
function deferred() { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; }
function fixture(overrides = {}) {
  const props = reactive({ accountId: 3, appleIdHint: "fixture@example.test", webAuthenticated: false, csrfToken: "fixture-csrf", busy: false });
  const events = [], calls = [];
  let dispose;
  const api = {
    getAppleAccountAuth: async id => ({ appleSession: { status: "login_required", appleId: `fixture-${id}@example.test` } }),
    loginAppleAccountAuth: async (...args) => { calls.push(["login", ...args]); return { status: "verification_required", challengeId: "fixture-challenge", appleSession: { status: "verification_required" } }; },
    verifyAppleAccountAuth: async (...args) => { calls.push(["verify", ...args]); return { status: "authenticated", appleSession: { status: "authenticated", appleId: "fixture@example.test" } }; },
    deleteAppleAccountAuth: async (...args) => { calls.push(["delete", ...args]); },
    requestAppleAccountVerificationCode: async (...args) => { calls.push(["send", ...args]); return { status: "verification_required", verification: { method: "sms", phone_id: 7, sent: true, phones: [{ id: 7, label: "受信任手机号 1" }] } }; },
    ...overrides,
  };
  const factory = Function("computed", "onBeforeUnmount", "ref", "watch", "defineProps", "defineEmits", "ElMessage", ...Object.keys(api), `${script}\nreturn { visible, loading, error, session, statusLabel, step, appleId, password, code, challengeId, verification, verificationMethod, verificationPhoneId, requestCode, openManager, closeManager, openLogin, login, verify, logoutAccount, manageWeb };`);
  const component = factory(computed, fn => { dispose = fn; }, ref, watch, () => props, () => (...args) => events.push(args), { success() {} }, ...Object.values(api));
  return { component, props, calls, events, dispose: () => dispose() };
}

test("unified connection completes device verification without directory login", async () => {
  const { component: c, calls, events, dispose } = fixture();
  await nextTick();
  c.openManager(); c.openLogin();
  c.password.value = "fixture-password";
  await c.login();
  assert.equal(c.step.value, "verification");
  assert.equal(c.password.value, "");
  assert.equal(c.challengeId.value, "fixture-challenge");
  assert.match(source, /v-if="step !== 'manage'"[^>]*@click="step === 'login' \? login\(\) : verify\(\)"/);
  c.code.value = "123456";
  await c.verify();
  assert.equal(c.step.value, "manage");
  assert.equal(c.session.value.status, "authenticated");
  assert.equal(c.code.value, "");
  assert.equal(c.challengeId.value, "");
  assert.deepEqual(calls.map(call => call[0]), ["login", "verify"]);
  assert.equal(events.some(event => event[0] === "open-web-login"), false);
  await c.logoutAccount();
  assert.equal(c.session.value, null);
  assert.equal(calls.at(-1)[0], "delete");
  assert.equal(events.some(event => event[0] === "disconnect-web"), false);
  dispose();
});

test("connection blocks duplicate login, clears secrets and ignores old-account replies", async () => {
  const response = deferred();
  let logins = 0;
  const { component: c, props, dispose } = fixture({ loginAppleAccountAuth: async () => { logins++; return response.promise; } });
  await nextTick(); c.openLogin(); c.password.value = "fixture-password";
  const pending = c.login();
  await c.login();
  assert.equal(logins, 1);
  let closed = false;
  c.closeManager(() => { closed = true; });
  assert.equal(closed, false);
  props.accountId = 4;
  await nextTick(); await nextTick();
  assert.equal(c.password.value, "");
  response.resolve({ status: "authenticated", appleSession: { status: "authenticated", appleId: "old-account@example.test" } });
  await pending;
  assert.equal(c.session.value.appleId, "fixture-4@example.test");
  assert.equal(c.visible.value, false);
  assert.equal(c.loading.value, false);
  dispose();
});

test("late session read cannot overwrite a fresh login checkpoint", async () => {
  const oldRead = deferred();
  const { component: c, props, events, calls, dispose } = fixture({ getAppleAccountAuth: async () => oldRead.promise });
  c.openLogin(); c.password.value = "fixture-password";
  props.busy = true;
  await c.login(); assert.equal(calls.length, 0);
  props.busy = false;
  await c.login(); c.code.value = "123456"; await c.verify();
  oldRead.resolve({ appleSession: { status: "login_required" } });
  await nextTick(); await nextTick();
  assert.equal(c.session.value.status, "authenticated");
  c.manageWeb("open-web-login");
  assert.equal(events.at(-1)[0], "open-web-login");
  assert.equal(calls.length, 2);
  c.password.value = "fixture-typed-secret";
  c.code.value = "123456";
  c.closeManager(() => {});
  assert.equal(c.password.value, ""); assert.equal(c.code.value, "");
  assert.doesNotMatch(script, /setInterval|setTimeout/);
  dispose();
});

test("temporary renewal failure remains distinct from a rejected login", async () => {
  const { component: c, dispose } = fixture({ getAppleAccountAuth: async () => ({ appleSession: {
    status: "expired", health: "retry_wait", nextRetryAt: "2026-10-16T08:00:00Z",
  } }) });
  await nextTick(); await nextTick();
  assert.equal(c.statusLabel.value, "等待续期重试");
  c.session.value = { status: "expired", health: "login_required" };
  assert.equal(c.statusLabel.value, "需重新登录");
  dispose();
});

test("Account connection selects SMS and submits a received code without resending", async () => {
  const { component: c, calls, dispose } = fixture({ loginAppleAccountAuth: async (...args) => {
    calls.push(["login", ...args]);
    return { status: "verification_required", challengeId: "pending", verification: { method: "device", sent: false, phones: [{ id: 7, label: "受信任手机号 1" }, { id: 9, label: "受信任手机号 2" }] } };
  } });
  await nextTick(); c.openLogin(); c.password.value = "fixture-password";
  await c.login();
  assert.equal(c.verification.value.phones.length, 2);
  c.verificationMethod.value = "sms"; c.verificationPhoneId.value = 9; c.code.value = "123456";
  await c.verify();
  assert.equal(c.step.value, "manage");
  assert.deepEqual(calls.map(item => item[0]), ["login", "verify"]);
  assert.deepEqual(calls[1][2], { challenge_id: "pending", code: "123456", verification_method: "sms", phone_id: 9 });
  assert.equal(c.verification.value, null);
  dispose();
});
