import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const componentPath = new URL("../src/components/AliasCreationPanel.vue", import.meta.url);
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
  assert.match(source, /creation-job-panel__apple/);
  assert.match(source, /creation-job-panel__controls \{ order: 2; margin-left: auto; \}/);
  assert.match(source, /creation-job-panel__status \{ display: flex; flex-basis: 100%;/);
  assert.match(source, /@media \(max-width: 720px\) \{ \.creation-job-panel__controls \{ margin-left: 0; \}/);
  assert.match(source, /const count = ref\(5\)/);
  assert.match(source, /const jobStatusPollIntervalMs = 10_000/);
  assert.match(source, /自动（初始选择通道）/);
  assert.match(source, /<details class="creation-job-panel__budget-details">/);
  assert.match(source, /1 小时最多 5 次、24 小时最多 20 次，尝试至少间隔 10 分钟/);
  assert.match(source, /100 次至少需要 5 天预算窗口；任务生命周期为 7 天/);
  assert.match(source, /自动只选择本轮初始通道，不因限流切换/);
  assert.match(source, /等待本地主号共享预算/);
  assert.match(source, /Apple 限流暂停中（至少 24 小时）/);
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
