import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const viewPath = new URL("../src/views/AccountDetailView.vue", import.meta.url);

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

function autoCreationErrorFormatter(source) {
  const messagesMatch = source.match(
    /const AUTO_CREATION_ERROR_MESSAGES = Object\.freeze\((\{[\s\S]*?\})\);/,
  );
  assert.ok(messagesMatch, "missing automatic creation error messages");
  const messages = Function(`"use strict"; return (${messagesMatch[1]});`)();
  const body = functionBody(source, "function autoCreationErrorMessage");
  const localBody = functionBody(source, "function isLocalCreationBudgetWait");
  const appleBody = functionBody(source, "function isAppleRateLimited");
  return Function(
    "AUTO_CREATION_ERROR_MESSAGES",
    "isLocalCreationBudgetWait",
    "isAppleRateLimited",
    `"use strict"; return function (value) ${body}`,
  )(
    messages,
    Function(`"use strict"; return function (value) ${localBody}`)(),
    Function(`"use strict"; return function (value) ${appleBody}`)(),
  );
}

test("account detail exposes automatic alias creation with persistent credential handling", async () => {
  const source = await readFile(viewPath, "utf8");

  assert.match(source, /<el-switch[\s\S]*自动创建隐私邮箱/);
  assert.match(source, /resumeAutoCreationAfterAuth/);
  assert.match(source, /autoCreation\.plannedTimes\?\.length/);
  assert.match(source, /autoCreation\.plannedAt/);
  assert.doesNotMatch(source, /getAliasAutoCreationKeys/);
  assert.doesNotMatch(source, /clearAliasAutoCreationKeys/);
  assert.doesNotMatch(source, /pendingAutoKeys|batchSecrets|OneTimeSecret/);

  assert.match(source, /copyAliasCredentials\(row, ALIAS_EXPORT_OTP\)/);
  assert.match(source, /copyAliasCredentials\(row, ALIAS_EXPORT_IMAP\)/);
  assert.match(
    functionBody(source, "async function rotateKey"),
    /旧 API Key、取码链接、IMAP 密码、refresh token 和访问令牌会同时失效/,
  );
  assert.match(
    functionBody(source, "async function rotateKey"),
    /邮件消费状态和 IMAP 已读状态保持不变/,
  );
  assert.match(
    functionBody(source, "async function rotateKey"),
    /alias\.credentialMode/,
  );

  const formatAutoCreationError = autoCreationErrorFormatter(source);
  assert.equal(
    formatAutoCreationError("APPLE_SESSION_EXPIRED"),
    "Apple 登录已过期，请点击“同步隐私邮箱”并重新登录后重试",
  );
  assert.equal(
    formatAutoCreationError("APPLE_RATE_LIMITED"),
    "Apple 返回了限流；该创建计划已暂停至少 24 小时，不会自动切换到另一通道。",
  );
  assert.match(
    formatAutoCreationError("APPLE_CREATION_BUDGET_WAIT"),
    /本项目的主号共享创建预算正在等待恢复.*不表示 Apple 返回了限流/,
  );
  assert.match(source, /自动收件连接已暂停.*App 专用密码.*邮箱服务状态/s);
  const authFailureBody = functionBody(source, "function isIMAPAuthenticationFailure");
  const isIMAPAuthenticationFailure = Function(
    `"use strict"; return function (value) ${authFailureBody}`,
  )();
  assert.equal(isIMAPAuthenticationFailure("login IMAP account: AUTHENTICATIONFAILED"), true);
  assert.equal(isIMAPAuthenticationFailure("IMAP_AUTHENTICATION_PAUSED"), true);
  assert.equal(isIMAPAuthenticationFailure("IMAP connection timeout"), false);
  const localBudgetBody = functionBody(source, "function isLocalCreationBudgetWait");
  const isLocalCreationBudgetWait = Function(
    `"use strict"; return function (value) ${localBudgetBody}`,
  )();
  const appleRateBody = functionBody(source, "function isAppleRateLimited");
  const isAppleRateLimited = Function(
    `"use strict"; return function (value) ${appleRateBody}`,
  )();
  assert.equal(isLocalCreationBudgetWait("本地主号创建预算已用尽，冷却后将自动继续"), true);
  assert.equal(isAppleRateLimited("Apple 请求被限流，当前周期剩余计划槽已跳过，冷却后会继续执行"), true);
  assert.equal(isAppleRateLimited("本地主号创建预算已用尽，请等待提示时间后重试"), false);
  assert.equal(
    formatAutoCreationError("APPLE_ALIAS_CONFIRMATION_PENDING"),
    "Apple 创建结果尚未完成目录确认；后续计划会继续确认，确认前不会重复创建",
  );
  assert.equal(
    formatAutoCreationError(" unknown upstream detail "),
    " unknown upstream detail ",
  );
});

test("account detail explains account-level suspension and automatic restoration", async () => {
  const source = await readFile(viewPath, "utf8");
  assert.match(source, /v-if="!account\.enabled" class="account-paused-notice"/);
  assert.match(source, /下属邮箱显示为暂停并暂停分配，同时移出邮箱池可分配库存/);
  assert.match(source, /按停用前快照恢复原启用及池成员状态/);
  assert.match(source, /单独停用的邮箱仍保持停用/);
  assert.match(source, /邮箱与历史记录未删除/);
});

test("batch creation panel is directly below the privacy-mail directory", async () => {
  const source = await readFile(viewPath, "utf8");
  const directory = source.indexOf('title="隐私邮箱"');
  const batch = source.indexOf("alias-creation-row");
  const automatic = source.indexOf('id="auto-creation-title"');
  assert.ok(directory >= 0 && batch > directory && automatic > batch);
  assert.match(source, /请使用上方批量任务/);
  assert.doesNotMatch(source, /请使用下方批量任务/);
});

test("account detail hides alias credential fields while retaining export actions", async () => {
  const source = await readFile(viewPath, "utf8");
  const template = source.slice(
    source.indexOf("<template>"),
    source.indexOf("<script setup>"),
  );
  const columnsMatch = source.match(
    /const aliasColumns = Object\.freeze\(\[([\s\S]*?)\]\);/,
  );
  assert.ok(columnsMatch, "missing account alias columns");

  for (const field of ["apiKey", "imapPassword", "clientId", "refreshToken"]) {
    assert.doesNotMatch(columnsMatch[1], new RegExp(`key: "${field}"`));
    assert.doesNotMatch(template, new RegExp(`column\.key === '${field}'`));
  }
  for (const label of ["API Key", "IMAP 密码", "client ID", "刷新令牌"]) {
    assert.doesNotMatch(template, new RegExp(`<dt>${label}</dt>`));
  }

  assert.match(template, /copyAliasCredentials\(row, ALIAS_EXPORT_OTP\)/);
  assert.match(template, /copyAliasCredentials\(row, ALIAS_EXPORT_IMAP\)/);
  assert.match(template, /copyAliasCredentials\(alias, ALIAS_EXPORT_OTP\)/);
  assert.match(template, /copyAliasCredentials\(alias, ALIAS_EXPORT_IMAP\)/);
});

test("account detail exposes the custom random mailbox generator separately", async () => {
  const source = await readFile(viewPath, "utf8");
  const generate = functionBody(source, "async function generateRandomAliases");

  assert.match(source, /自动生成随机邮箱/);
  assert.match(source, /v-model="randomAliasCount"/);
  assert.match(source, /createRandomAliases\(/);
  assert.match(source, /8–12 位随机英文数字/);
  assert.match(source, /isCustomMailbox/);
  assert.match(source, /单次最多 1000 个/);
  assert.match(source, /可多次分批生成/);
  assert.match(source, /自定义邮箱主号的累计数量不设上限/);
  assert.match(
    source,
    /:disabled="randomAliasLoading \|\| syncLoading \|\| syncActive \|\| !account\.enabled \|\| !account\.emailSuffix"/,
  );
  assert.match(generate, /if \(!account\.value\.enabled\)/);
  assert.match(generate, /主号已停用，不能生成随机邮箱/);
});

test("custom account deletion identifies the mailbox by its suffix", async () => {
  const source = await readFile(viewPath, "utf8");
  const remove = functionBody(source, "async function removeAccount");

  assert.match(remove, /isCustomMailbox\.value/);
  assert.match(remove, /`@\$\{account\.value\.emailSuffix\}`/);
  assert.match(remove, /: account\.value\.email/);
  assert.match(remove, /确定删除主号 \$\{accountIdentity\}/);
});

test("directory-confirmation aliases remain visibly gated without a key-claim queue", async () => {
  const source = await readFile(viewPath, "utf8");
  const confirmationBody = functionBody(
    source,
    "function isAliasConfirmationPending",
  );
  const isAliasConfirmationPending = Function(
    `"use strict"; return function (item) ${confirmationBody}`,
  )();

  assert.equal(
    isAliasConfirmationPending({
      enabled: false,
      lastSyncError: "APPLE_ALIAS_CONFIRMATION_PENDING",
    }),
    true,
  );
  assert.equal(
    isAliasConfirmationPending({
      enabled: true,
      lastSyncError: "APPLE_ALIAS_CONFIRMATION_PENDING",
    }),
    false,
  );
  for (const signature of [
    "async function rotateKey",
    "async function copyAliasCredentials",
    "async function toggleAlias",
    "async function removeAlias",
  ]) {
    assert.match(
      functionBody(source, signature),
      /isAliasConfirmationPending\(alias\)/,
      `${signature} must reject a directory-confirmation alias`,
    );
  }
});

test("alias deletion is presented as an irreversible iCloud operation", async () => {
  const source = await readFile(viewPath, "utf8");
  const remove = functionBody(source, "async function removeAlias");

  assert.match(source, /从 iCloud 永久删除隐私邮箱/);
  assert.match(remove, /将从 iCloud 永久删除/);
  assert.match(remove, /且无法恢复/);
  assert.match(remove, /await deleteAlias\(alias\.id/);
  assert.match(remove, /await loadDetail\(\)/);
  assert.doesNotMatch(remove, /aliases\.value = aliases\.value\.filter/);
  assert.match(remove, /isAppleSessionInvalid\(error\)/);
  assert.match(remove, /openAppleLogin\(\{ error \}\)/);
  assert.match(remove, /本地记录已保留/);
});
