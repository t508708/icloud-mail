import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const aliasesViewPath = new URL(
  "../src/views/AliasesView.vue",
  import.meta.url,
);
const auditViewPath = new URL("../src/views/AuditView.vue", import.meta.url);

function functionBody(source, signature) {
  const start = source.indexOf(signature);
  assert.notEqual(start, -1, `missing ${signature}`);
  const opening = source.indexOf("{", start);
  assert.notEqual(opening, -1, `missing body for ${signature}`);
  let depth = 0;
  for (let index = opening; index < source.length; index += 1) {
    if (source[index] === "{") depth += 1;
    if (source[index] === "}") {
      depth -= 1;
      if (depth === 0) return source.slice(opening, index + 1);
    }
  }
  assert.fail(`unterminated body for ${signature}`);
}

function extractedFunction(source, signature, dependencies = {}) {
  const body = functionBody(source, signature);
  const name = signature.split(" ").at(-1);
  const names = Object.keys(dependencies);
  return Function(
    ...names,
    `"use strict"; return function ${name}(error) ${body}`,
  )(...names.map((key) => dependencies[key]));
}

function rotationHandler(source, dependencies) {
  const body = functionBody(source, "async function rotateAllCredentials");
  const names = Object.keys(dependencies);
  return Function(
    ...names,
    `"use strict"; return async function rotateAllCredentials() ${body}`,
  )(...names.map((name) => dependencies[name]));
}

test("all-alias rotation is a locked danger action with password and phrase confirmations", async () => {
  const source = await readFile(aliasesViewPath, "utf8");
  const template = source.slice(
    source.indexOf("<template>"),
    source.indexOf("<script setup>"),
  );
  const marker = template.indexOf(
    'aria-label="轮换所有隐私邮箱令牌与凭证"',
  );
  assert.notEqual(marker, -1);
  const button = template.slice(
    template.lastIndexOf("<el-button", marker),
    template.indexOf("</el-button>", marker) + "</el-button>".length,
  );
  const body = functionBody(source, "async function rotateAllCredentials");

  assert.match(button, /type="danger"/);
  assert.match(button, /:loading="rotatingAllCredentials"/);
  assert.match(button, /:disabled="rotatingAllCredentials \|\| exportingAll"/);
  assert.match(button, /@click="rotateAllCredentials"/);
  assert.match(button, /轮换全部令牌/);
  assert.equal((body.match(/await ElMessageBox\.prompt\(/g) || []).length, 2);
  assert.doesNotMatch(body, /ElMessageBox\.confirm/);
  assert.match(body, /inputType: "password"/);
  assert.match(body, /所有旧 V1 直达链接/);
  assert.match(body, /V2 取码令牌、API Key、IMAP 密码和 OAuth 凭据/);
  assert.match(body, /旧版邮箱会强制升级为 V2/);
  assert.match(body, /Apple 确认中的邮箱也会同步轮换/);
  assert.match(body, /所有后台会话将被撤销/);
  assert.match(body, /value === "ROTATE_ALL"/);
  assert.match(body, /currentPassword = ""/);
  assert.doesNotMatch(body, /selectedAccountId|appliedGroupId|appliedAliasQuery/);
  assert.doesNotMatch(body, /loadAliases/);
  assert.doesNotMatch(
    body,
    /summary\.(?:apiKey|imapPassword|clientId|refreshToken|otpUrlPath)/,
  );
  assert.match(
    functionBody(source, "function copyAllAliases"),
    /exportingAll\.value \|\| rotatingAllCredentials\.value/,
  );
  assert.match(
    source,
    /async function loadAliases\(\{ silent = false \} = \{\}\) \{\s*if \(rotatingAllCredentials\.value\) return/,
  );
  assert.match(
    template,
    /:disabled="total === 0 \|\| exportingAll \|\| rotatingAllCredentials"/,
  );
});

test("all-alias rotation submits once, clears local data, revokes auth, and reports five counts", async () => {
  const source = await readFile(aliasesViewPath, "utf8");
  const rotatingAllCredentials = { value: false };
  const aliases = {
    value: [
      {
        id: 1,
        apiKey: "old-api-key",
        imapPassword: "old-imap-password",
        refreshToken: "old-refresh-token",
        otpUrlPath: "/api/v1/otp?token=old-token",
      },
    ],
  };
  const total = { value: 1 };
  const events = [];
  let apiCalls = 0;
  let promptIndex = 0;
  const handler = rotationHandler(source, {
    rotatingAllCredentials,
    exportingAll: { value: false },
    ElMessageBox: {
      prompt: async (_message, _title, options) => {
        promptIndex += 1;
        if (promptIndex === 1) {
          events.push("prompt:password");
          assert.equal(options.inputType, "password");
          assert.equal(options.inputValidator("current-password"), true);
          assert.equal(
            options.inputValidator(""),
            "请输入当前管理员密码",
          );
          return { value: "current-password" };
        }
        events.push("prompt:confirmation");
        assert.equal(options.inputValidator("ROTATE_ALL"), true);
        assert.equal(
          options.inputValidator("rotate_all"),
          "请输入完整的 ROTATE_ALL",
        );
        return { value: "ROTATE_ALL" };
      },
    },
    liveRefresh: { stop: () => events.push("stop") },
    beginAliasMutation: () => events.push("begin"),
    rotateAllAliasCredentials: async (currentPassword, csrfToken) => {
      apiCalls += 1;
      assert.equal(currentPassword, "current-password");
      assert.equal(csrfToken, "csrf-token");
      events.push("api");
      return {
        total: 9,
        rotated: 9,
        migratedLegacy: 3,
        rotatedV2: 6,
        rotatedPending: 2,
        reauthenticationRequired: true,
      };
    },
    auth: {
      state: { csrfToken: "csrf-token" },
      clearSession: (options) => {
        assert.deepEqual(options, { checked: false });
        events.push("clear-session");
      },
    },
    viewActive: true,
    aliases,
    total,
    clearAliasSelection: () => events.push("clear-selection"),
    successMessage: (message, duration) => {
      assert.equal(duration, 10000);
      events.push(`success:${message}`);
    },
    router: {
      replace: async (route) => {
        assert.deepEqual(route, {
          name: "login",
          query: { notice: "credentials_rotated" },
        });
        events.push("redirect");
      },
    },
    confirmationCancelled: (error) => error === "cancel" || error === "close",
    rotateAllCredentialsErrorMessage: () => "request failed",
    showRequestError: (error) => events.push(`error:${error}`),
  });

  const first = handler();
  const duplicate = handler();
  await Promise.all([first, duplicate]);

  assert.equal(apiCalls, 1);
  assert.equal(rotatingAllCredentials.value, false);
  assert.deepEqual(events.slice(0, 6), [
    "prompt:password",
    "prompt:confirmation",
    "stop",
    "begin",
    "api",
    "clear-selection",
  ]);
  assert.match(events[6], /^success:/);
  assert.deepEqual(events.slice(7), ["clear-session", "redirect"]);
  assert.deepEqual(aliases.value, []);
  assert.equal(total.value, 0);
  assert.match(events[6], /共检查 9 个/);
  assert.match(events[6], /成功轮换 9 个/);
  assert.match(events[6], /V1 升级 V2 3 个/);
  assert.match(events[6], /现有 V2 已轮换 6 个/);
  assert.match(events[6], /Apple 确认中同步轮换 2 个/);
  assert.match(events[6], /新凭据未自动加载/);
  assert.match(events[6], /后台会话已撤销/);
});

test("cancelling either confirmation never calls or pauses the rotation", async () => {
  const source = await readFile(aliasesViewPath, "utf8");
  for (const cancelledPrompt of [1, 2]) {
    const rotatingAllCredentials = { value: false };
    let promptIndex = 0;
    let apiCalls = 0;
    const handler = rotationHandler(source, {
      rotatingAllCredentials,
      exportingAll: { value: false },
      ElMessageBox: {
        prompt: async () => {
          promptIndex += 1;
          if (promptIndex === cancelledPrompt) {
            throw cancelledPrompt === 1 ? "cancel" : "close";
          }
          return { value: "current-password" };
        },
      },
      liveRefresh: { stop: () => assert.fail("cancel stopped live refresh") },
      beginAliasMutation: () => assert.fail("cancel began mutation"),
      rotateAllAliasCredentials: async () => {
        apiCalls += 1;
        return {};
      },
      auth: { state: { csrfToken: "csrf-token" } },
      viewActive: true,
      aliases: { value: [] },
      total: { value: 0 },
      clearAliasSelection: () => {},
      successMessage: () => {},
      router: { replace: async () => {} },
      confirmationCancelled: (error) =>
        error === "cancel" || error === "close",
      rotateAllCredentialsErrorMessage: () => "request failed",
      showRequestError: () => assert.fail("cancellation must stay silent"),
    });

    await handler();
    assert.equal(apiCalls, 0, `prompt ${cancelledPrompt} called the API`);
    assert.equal(rotatingAllCredentials.value, false);
  }
});

test("rotation errors are sanitized and never clear an otherwise valid session", async () => {
  const source = await readFile(aliasesViewPath, "utf8");
  const formatError = extractedFunction(
    source,
    "function rotateAllCredentialsErrorMessage",
  );
  assert.equal(
    formatError({ code: "CURRENT_PASSWORD_INVALID" }),
    "当前管理员密码验证失败，请重新输入。",
  );
  assert.equal(
    formatError({ code: "RATE_LIMITED" }),
    "安全验证尝试过于频繁，请 15 分钟后再试。",
  );

  const rotatingAllCredentials = { value: false };
  const aliases = { value: [{ id: 1, apiKey: "old-api-key" }] };
  const requestError = {
    code: "CURRENT_PASSWORD_INVALID",
    message: "upstream detail must not be displayed",
  };
  let promptIndex = 0;
  let feedback = null;
  let liveRefreshRestarted = false;
  const handler = rotationHandler(source, {
    rotatingAllCredentials,
    exportingAll: { value: false },
    ElMessageBox: {
      prompt: async () => {
        promptIndex += 1;
        return { value: promptIndex === 1 ? "wrong-password" : "ROTATE_ALL" };
      },
    },
    liveRefresh: {
      stop: () => {},
      start: (options) => {
        assert.deepEqual(options, { immediate: false });
        liveRefreshRestarted = true;
      },
    },
    beginAliasMutation: () => {},
    rotateAllAliasCredentials: async () => {
      throw requestError;
    },
    auth: {
      state: { csrfToken: "csrf-token" },
      clearSession: () => assert.fail("failed verification cleared session"),
    },
    viewActive: true,
    aliases,
    total: { value: 1 },
    clearAliasSelection: () => assert.fail("failed rotation cleared selection"),
    successMessage: () => assert.fail("failed rotation reported success"),
    router: { replace: async () => assert.fail("failed rotation redirected") },
    confirmationCancelled: () => false,
    rotateAllCredentialsErrorMessage: formatError,
    showRequestError: (error, fallback) => {
      feedback = { error, fallback };
    },
  });

  await handler();

  assert.deepEqual(feedback, {
    error: {
      code: "CURRENT_PASSWORD_INVALID",
      message: "当前管理员密码验证失败，请重新输入。",
    },
    fallback: "当前管理员密码验证失败，请重新输入。",
  });
  assert.equal(rotatingAllCredentials.value, false);
  assert.equal(liveRefreshRestarted, true);
  assert.deepEqual(aliases.value, [{ id: 1, apiKey: "old-api-key" }]);
});

test("a concurrent credential change clears local state and redirects to login", async () => {
  const source = await readFile(aliasesViewPath, "utf8");
  const aliases = { value: [{ id: 1, apiKey: "old-api-key" }] };
  const total = { value: 1 };
  let promptIndex = 0;
  let sessionCleared = false;
  let redirected = false;
  const handler = rotationHandler(source, {
    rotatingAllCredentials: { value: false },
    exportingAll: { value: false },
    ElMessageBox: {
      prompt: async () => {
        promptIndex += 1;
        return { value: promptIndex === 1 ? "current-password" : "ROTATE_ALL" };
      },
    },
    liveRefresh: { stop: () => {} },
    beginAliasMutation: () => {},
    rotateAllAliasCredentials: async () => {
      throw { code: "CREDENTIALS_CHANGED" };
    },
    auth: {
      state: { csrfToken: "csrf-token" },
      clearSession: (options) => {
        assert.deepEqual(options, { checked: false });
        sessionCleared = true;
      },
    },
    viewActive: true,
    aliases,
    total,
    clearAliasSelection: () => {},
    successMessage: () => assert.fail("concurrent change reported success"),
    router: {
      replace: async (route) => {
        assert.deepEqual(route, {
          name: "login",
          query: { notice: "credentials_changed" },
        });
        redirected = true;
      },
    },
    confirmationCancelled: () => false,
    rotateAllCredentialsErrorMessage: () => "request failed",
    showRequestError: () => assert.fail("concurrent change showed request error"),
  });

  await handler();

  assert.equal(sessionCleared, true);
  assert.equal(redirected, true);
  assert.deepEqual(aliases.value, []);
  assert.equal(total.value, 0);
});

test("uncertain or malformed rotation results clear secrets and require a fresh login", async () => {
  const source = await readFile(aliasesViewPath, "utf8");
  for (const [code, notice] of [
    ["ROTATION_RESULT_INVALID", "rotation_result_invalid"],
    ["NETWORK_ERROR", "rotation_status_unknown"],
  ]) {
    const aliases = { value: [{ id: 1, apiKey: "old-api-key" }] };
    const total = { value: 1 };
    let promptIndex = 0;
    let redirectedNotice = "";
    let sessionCleared = false;
    const handler = rotationHandler(source, {
      rotatingAllCredentials: { value: false },
      exportingAll: { value: false },
      ElMessageBox: {
        prompt: async () => {
          promptIndex += 1;
          return {
            value: promptIndex === 1 ? "current-password" : "ROTATE_ALL",
          };
        },
      },
      liveRefresh: {
        stop: () => {},
        start: () => assert.fail(`${code} restarted live refresh`),
      },
      beginAliasMutation: () => {},
      rotateAllAliasCredentials: async () => {
        throw { code };
      },
      auth: {
        state: { csrfToken: "csrf-token" },
        clearSession: () => {
          sessionCleared = true;
        },
      },
      viewActive: true,
      aliases,
      total,
      clearAliasSelection: () => {},
      successMessage: () => assert.fail(`${code} reported success`),
      router: {
        replace: async (route) => {
          redirectedNotice = route.query.notice;
        },
      },
      confirmationCancelled: () => false,
      rotateAllCredentialsErrorMessage: () => "request failed",
      showRequestError: () => assert.fail(`${code} stayed on the old page`),
    });

    await handler();

    assert.equal(sessionCleared, true);
    assert.equal(redirectedNotice, notice);
    assert.deepEqual(aliases.value, []);
    assert.equal(total.value, 0);
  }
});

test("an active full export blocks rotation before any confirmation", async () => {
  const source = await readFile(aliasesViewPath, "utf8");
  let prompted = false;
  let apiCalls = 0;
  const handler = rotationHandler(source, {
    rotatingAllCredentials: { value: false },
    exportingAll: { value: true },
    ElMessageBox: {
      prompt: async () => {
        prompted = true;
      },
    },
    rotateAllAliasCredentials: async () => {
      apiCalls += 1;
    },
  });

  await handler();

  assert.equal(prompted, false);
  assert.equal(apiCalls, 0);
});

test("audit view translates the global credential-rotation action", async () => {
  const source = await readFile(auditViewPath, "utf8");
  assert.match(source, /auditActionLabel\(action\)/);
  const { auditActionLabel } = await import("../src/utils/audit.js");
  assert.equal(auditActionLabel("rotate_all_credentials"), "轮换全部凭证");
});
