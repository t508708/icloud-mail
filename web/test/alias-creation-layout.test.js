import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const componentPath = new URL("../src/components/AliasCreationPanel.vue", import.meta.url);
const connectionPath = new URL("../src/components/AppleConnectionPanel.vue", import.meta.url);
const viewPath = new URL("../src/views/AccountDetailView.vue", import.meta.url);

function functionBody(source, signature) {
  const start = source.indexOf(signature);
  assert.notEqual(start, -1, `missing ${signature}`);
  const opening = source.indexOf("{", start);
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

test("batch creation stays compact without progress or created-address summary", async () => {
  const source = await readFile(componentPath, "utf8");
  assert.doesNotMatch(source, /el-progress|已创建\s*\{\{/);
  assert.match(source, /creation-job-panel__title/);
  assert.doesNotMatch(source, /creation-job-panel__apple|el-dialog|loginAppleAccountAuth|v-model="channel"/);
  assert.match(source, /channel: "auto"/);
  assert.match(source, /管理 Apple 连接/);
  assert.match(source, /creation-job-panel__controls \{ order: 2; margin-left: auto; \}/);
  assert.match(source, /creation-job-panel__status \{ display: flex; flex-basis: 100%;/);
  assert.match(source, /@media \(max-width: 720px\) \{ \.creation-job-panel__controls \{ margin-left: 0; \}/);
  assert.match(source, /const count = ref\(5\)/);
  assert.match(source, /const jobStatusPollIntervalMs = 10_000/);
  assert.match(source, /<details class="creation-job-panel__budget-details">/);
  assert.match(source, /手动批量创建和单次探测均不使用本地创建额度/);
  assert.match(source, /任务生命周期为 7 天/);
  assert.match(source, /自动只选择本轮初始通道，不因限流切换/);
  assert.match(source, /Apple 限流暂停中（至少 24 小时）/);
  assert.match(source, /手动探测 1 个/);
  assert.match(source, /createAliasNow/);
  assert.match(source, /本地主号创建预算已用尽，本地等待不表示 Apple 返回了限流/);
  const localWaitBody = functionBody(source, "function isLocalBudgetWait");
  const isLocalBudgetWait = Function(`"use strict"; return function (value) ${localWaitBody}`)();
  const appleWaitBody = functionBody(source, "function isAppleRateLimited");
  const isAppleRateLimited = Function(`"use strict"; return function (value) ${appleWaitBody}`)();
  assert.equal(isLocalBudgetWait("本地主号创建预算已用尽，请等待提示时间后重试"), true);
  assert.equal(isAppleRateLimited("Apple 请求过于频繁，请稍后再试"), true);
  assert.equal(isAppleRateLimited("本地主号创建预算已用尽，冷却后将自动继续"), false);
});

test("batch creation is a sibling section of the Apple directory section", async () => {
  const source = await readFile(viewPath, "utf8");
  const directory = source.indexOf('title="隐私邮箱"');
  const batch = source.indexOf("alias-creation-row");
  const batchSectionEnd = source.indexOf("</section>", batch);
  const following = source.indexOf('<section class="section-block">', batchSectionEnd);
  assert.ok(directory >= 0 && batch > directory && following > batch);
  assert.match(source, /v-if="!isCustomMailbox" class="section-block alias-creation-row"/);
});

test("manual probe runs once during batch wait, refreshes counts and never changes the job", async () => {
  const source = await readFile(componentPath, "utf8");
  const body = functionBody(source, "async function probe");
  const props = { accountId: 7, csrfToken: "csrf", accountEnabled: true, webAuthenticated: true };
  const job = { value: { status: "waiting", completed: 3 } };
  const probing = { value: false }, starting = { value: false }, ready = { value: true };
  const probeError = { value: null };
  let calls = 0, current = true, failure = null;
  const changes = [], messages = [];
  const create = async (id, token, selected) => { calls++; assert.equal(id, 7); assert.equal(token, "csrf"); assert.equal(selected, "auto"); if (failure) throw failure; };
  const probe = Function("props", "job", "probing", "starting", "ready", "probeError", "guard", "createAliasNow", "emit", "ElMessage", `return async function () ${body}`)(
    props, job, probing, starting, ready, probeError,
    () => () => current, create, (...event) => changes.push(event), { success: message => messages.push(message) },
  );
  await probe();
  assert.equal(calls, 1); assert.deepEqual(changes, [["change", { account_id: 7 }]]);
  assert.deepEqual(job.value, { status: "waiting", completed: 3 }); assert.equal(probing.value, false);
  failure = { code: "APPLE_RATE_LIMITED", message: "throttled" };
  await probe();
  assert.equal(calls, 2); assert.equal(probeError.value, failure); assert.equal(changes.length, 1);
  failure = { code: "APPLE_ALIAS_CONFIRMATION_PENDING", message: "pending" };
  await probe();
  assert.equal(calls, 3); assert.deepEqual(changes[1], ["change", { account_id: 7 }]);
  job.value.status = "running";
  await probe(); assert.equal(calls, 3);
  job.value.status = "waiting"; props.connectionBusy = true;
  await probe(); assert.equal(calls, 3);
  props.connectionBusy = false; props.accountEnabled = false;
  await probe(); assert.equal(calls, 3);
  props.accountEnabled = true; props.webAuthenticated = false;
  await probe(); assert.equal(calls, 3);
  props.webAuthenticated = true; failure = null; current = false;
  await probe(); assert.equal(calls, 4); assert.equal(changes.length, 2); assert.equal(messages.length, 1);
});

test("new Apple Account login is independent of old web auth and account state", async () => {
  const source = await readFile(connectionPath, "utf8");
  assert.match(source, /const blocked = computed\(\(\) => props.busy \|\| loading.value\)/);
  assert.doesNotMatch(functionBody(source, "async function login"), /webAuthenticated|accountEnabled/);
  assert.match(source, /两套会话独立保存，分别验证/);
  assert.match(source, /登录仅配置连接，不会启用已停用的主号/);
  const batch = await readFile(componentPath, "utf8");
  assert.match(batch, /job.value\?\.status === "running" \|\| starting.value \|\| probing.value, busy => emit\("operation-busy", busy\)/);
  assert.match(batch, /:disabled="probing \|\| starting \|\| job\?\.status === 'running' \|\| !ready \|\| !accountEnabled \|\| !webAuthenticated \|\| connectionBusy"/);
  const view = await readFile(viewPath, "utf8");
  assert.match(view, /@operation-busy="\(busy\) => \(batchOperationLoading = busy\)"/);
  assert.match(view, /:busy="appleConnectionBlocked"/);
});
