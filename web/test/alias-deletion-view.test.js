import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { computed, createSSRApp, h, ref } from "vue";
import { renderToString } from "vue/server-renderer";

import { normalizeAliasDeletionJob } from "../src/api/admin.js";
import {
  ALIAS_DELETION_OPERATION_LABELS,
  formatAliasDeletionResultMessage,
  isAliasDeletionJobActive,
  isAliasDeletionJobTerminal,
} from "../src/utils/aliasDeletionJob.js";
import { formatTime } from "../src/utils/format.js";

const viewPath = new URL("../src/views/AliasesView.vue", import.meta.url);
const source = await readFile(viewPath, "utf8");
const deleteFunctions = source.slice(
  source.indexOf("function batchDeleteAccountState("),
  source.indexOf("async function copyAliases("),
);
const progressTemplate = source.match(/<section\s+v-if="deletionProgressVisible"[\s\S]*?<\/section>/)[0];
const progressComputeds = source.slice(
  source.indexOf("const deletionJob = computed("),
  source.indexOf("function makeDeletionController("),
);

async function renderProgress(rawJob, statePatch = {}) {
  const deletionState = ref({ job: normalizeAliasDeletionJob(rawJob), ...statePatch });
  const values = Function("computed", "deletionState", `
    ${progressComputeds}
    return { deletionJob, deletionPercentage, deletionRecentFailures, deletionWaits,
      deletionJobType, deletionStatusLabel };
  `)(computed, deletionState);
  const app = createSSRApp({
    template: progressTemplate,
    setup: () => ({
      ...values, deletionState, deletionResultsExpanded: ref(true), deletingAliases: false,
      deletionProgressVisible: true, deletionVisibility: { dismiss() {} },
      ALIAS_DELETION_OPERATION_LABELS, formatAliasDeletionResultMessage,
      isAliasDeletionJobActive, isAliasDeletionJobTerminal, formatTime, Refresh: null,
      refreshDeletionJob() {}, acknowledgeDeletionState() {},
    }),
  });
  for (const name of ["el-tag", "el-button", "el-progress"]) {
    app.component(name, { setup: (_props, { slots }) => () => h("span", slots.default?.()) });
  }
  app.config.warnHandler = (message) => assert.fail(message);
  const html = await renderToString(app);
  return { html, text: html.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ") };
}

function harness(overrides = {}) {
  const events = [];
  const warnings = [];
  const errors = [];
  const dependencies = {
    viewActive: true,
    deletingAliases: { value: false },
    deletionJobBlocked: { value: false },
    movingAliases: { value: false },
    exportingAll: { value: false },
    rotatingAllCredentials: { value: false },
    selectedAliasIds: { value: [91, 92] },
    aliases: { value: [{ id: 91, accountId: 1 }, { id: 92, accountId: 2 }] },
    auth: { state: { username: "owner", csrfToken: "csrf" } },
    deletionController: {
      submit: async (ids, csrf) => {
        events.push("submit");
        assert.deepEqual(ids, [91, 92]);
        assert.equal(csrf, "csrf");
        return true;
      },
    },
    getAccount: async (id, options) => {
      events.push(`account:${id}`);
      assert.deepEqual(options, { limit: 1, offset: 0 });
      return { account: { mailboxType: "icloud", lastSyncStatus: "success" }, appleSession: { status: "authenticated" } };
    },
    ElMessageBox: {
      confirm: async (_message, _title, options) => {
        events.push("confirm");
        assert.equal(options.confirmButtonClass, "el-button--danger");
      },
      prompt: async (_message, _title, options) => {
        events.push("prompt");
        assert.equal(options.inputValidator("DELETE_APPLE_ALIASES"), true);
        assert.notEqual(options.inputValidator("delete_apple_aliases"), true);
        assert.equal(options.confirmButtonClass, "el-button--danger");
      },
    },
    ElMessage: { warning: (message) => warnings.push(message) },
    clearAliasSelection: () => events.push("clear"),
    isAliasConfirmationPending: (alias) => alias.pending === true,
    liveRefresh: { stop: () => events.push("stop"), start: () => events.push("restart") },
    beginAliasMutation: () => events.push("begin"),
    confirmationCancelled: (error) => error === "cancel" || error === "close",
    showRequestError: (error, fallback) => errors.push({ error, fallback }),
    successMessage: () => assert.fail("submission is not evidence of successful deletion"),
    ...overrides,
  };
  const extracted = Function(...Object.keys(dependencies), `
    "use strict";
    ${deleteFunctions}
    return {
      run: deleteSelectedAliases,
      setActive(value) { viewActive = value; },
      replaceController(value) { deletionController = value; },
    };
  `)(...Object.values(dependencies));
  return { ...extracted, dependencies, events, warnings, errors };
}

test("eligibility checks and both dangerous confirmations precede exactly one async submit", async () => {
  const { run, events, dependencies, errors } = harness();
  await Promise.all([run(), run()]);
  assert.deepEqual(events, ["stop", "begin", "account:1", "account:2", "confirm", "prompt", "submit", "clear", "restart"]);
  assert.equal(dependencies.deletingAliases.value, false);
  assert.deepEqual(errors, []);
  assert.doesNotMatch(deleteFunctions, /deleteAliases\(|Math\.max|selected\.length -|result\?\.(deleted|failed)|本地记录已保留/);
});

test("each distinct account is checked once and a blocked recovery never opens confirmation", async () => {
  const sameAccount = harness({ aliases: { value: [{ id: 91, accountId: 1 }, { id: 92, accountId: 1 }] } });
  await sameAccount.run();
  assert.deepEqual(sameAccount.events.filter((event) => event.startsWith("account:")), ["account:1"]);
  for (const key of ["deletingAliases", "deletionJobBlocked", "movingAliases", "exportingAll", "rotatingAllCredentials"]) {
    const blocked = harness({ [key]: { value: true } });
    await blocked.run();
    assert.deepEqual(blocked.events, []);
  }
});

test("stale selection, Apple-directory pending, custom, errored and logged-out accounts stay blocked", async () => {
  const stale = harness({ selectedAliasIds: { value: [91, 99] } });
  await stale.run();
  assert.deepEqual(stale.events, ["clear"]);
  assert.equal(stale.errors.length, 1);
  const pending = harness({ aliases: { value: [{ id: 91, accountId: 1, pending: true }, { id: 92, accountId: 2 }] } });
  await pending.run();
  assert.deepEqual(pending.events, []);
  assert.equal(pending.warnings.length, 1);
  for (const detail of [
    { account: { mailboxType: "custom" } },
    { account: { mailboxType: "icloud", lastSyncStatus: "error" } },
    { account: { mailboxType: "icloud", lastSyncError: "sync error" } },
    { account: { mailboxType: "icloud" }, appleSession: { status: "expired" } },
  ]) {
    const blocked = harness({ getAccount: async () => detail });
    await blocked.run();
    assert.deepEqual(blocked.events, ["stop", "begin", "restart"]);
    assert.equal(blocked.warnings.length, 1);
  }
});

test("cancelling either dangerous confirmation never submits", async () => {
  for (const stage of ["confirm", "prompt"]) {
    const cancelled = harness({ ElMessageBox: {
      confirm: async () => { if (stage === "confirm") throw "cancel"; },
      prompt: async () => { throw "close"; },
    } });
    await cancelled.run();
    assert.equal(cancelled.events.includes("submit"), false);
    assert.deepEqual(cancelled.errors, []);
    assert.equal(cancelled.events.at(-1), "restart");
    assert.equal(cancelled.dependencies.deletingAliases.value, false);
  }
});

test("unmount or administrator changes during preflight/confirmations prevent a stale submit", async () => {
  for (const stage of ["preflight", "confirm", "prompt"]) {
    for (const change of ["unmount", "username", "controller"]) {
      let view;
      const invalidate = () => {
        if (change === "unmount") view.setActive(false);
        if (change === "username") view.dependencies.auth.state.username = "another";
        if (change === "controller") view.replaceController({ submit: () => assert.fail("new administrator received stale selection") });
      };
      view = harness(stage === "preflight" ? {
        getAccount: async () => {
          await Promise.resolve();
          invalidate();
          return { account: { mailboxType: "icloud" }, appleSession: { status: "authenticated" } };
        },
      } : {});
      if (stage !== "preflight") {
        view.dependencies.ElMessageBox[stage] = async () => invalidate();
      }
      await view.run();
      assert.equal(view.events.includes("submit"), false, `${stage}/${change} submitted`);
      assert.deepEqual(view.errors, []);
      if (change === "unmount") assert.equal(view.events.includes("restart"), false);
    }
  }
});

test("rejected submissions show the request error without claiming mailbox outcomes", async () => {
  const error = { status: 429, code: "BATCH_DELETE_BUSY", message: "后台任务已满" };
  const view = harness({ deletionController: { submit: async () => { throw error; } } });
  await view.run();
  assert.equal(view.errors[0].error, error);
  assert.equal(view.events.includes("clear"), false);
  assert.doesNotMatch(view.errors[0].fallback, /本地记录已保留|删除失败|已删除/);
  const raced = harness({ deletionController: { submit: async () => false } });
  await raced.run();
  assert.equal(raced.events.includes("clear"), false);
  assert.equal(raced.warnings.length, 1);
});

test("page lifecycle starts recovery, stops polling, and isolates controllers on username changes", () => {
  assert.match(source, /onMounted\(\(\) => \{\s*if \(auth\.state\.username\) void deletionController\.start\(\)/);
  assert.match(source, /onBeforeUnmount\(\(\) => \{\s*viewActive = false;\s*deletionController\.stop\(\)/);
  assert.match(source, /watch\(\(\) => auth\.state\.username, \(\) => \{\s*deletionController\.stop\(\);\s*deletionController = makeDeletionController\(\)/);
  assert.match(source, /createAliasDeletionStorage\(ADMIN_BASE_PATH, username\)/);
  assert.match(source, /auth\.state\.username !== username/);
  assert.match(source, /deletionState\.operationId/);
  for (const field of ["processed", "requested", "deleted", "failed"]) {
    assert.match(source, new RegExp(`\\{\\{ deletionJob\\.${field} \\}\\}`));
  }
  assert.match(source, /formatAliasDeletionResultMessage\(failure\)/);
  assert.match(source, /formatAliasDeletionResultMessage\(result\)/);
  assert.doesNotMatch(progressTemplate, /本地记录已保留|v-html|dangerouslyUseHTMLString|raw_?body/i);
  assert.match(source, /任务已中断，部分 Apple 结果待确认/);
  assert.match(source, /不会自动重放剩余项/);
  assert.doesNotMatch(source, /删除失败，结果待核对|近期失败原因/);
  assert.match(source, /\.alias-deletion-progress\s*\{[^}]*overflow:\s*auto;[^}]*overflow-wrap:\s*anywhere;/);
  assert.match(source, /\.alias-deletion-progress__header\s*\{[^}]*flex-wrap:\s*wrap;/);
});

test("terminal transitions clear selection and reload lists once, late cross-user callbacks are ignored", () => {
  const factory = source.slice(source.indexOf("function makeDeletionController("), source.indexOf("watch(() => auth.state.username"));
  const refreshes = [];
  const state = { value: {} };
  const auth = { state: { username: "owner" } };
  let options;
  const dependencies = {
    auth, viewActive: true, deletionState: state, deletionResultsExpanded: { value: false },
    startAliasDeletionJob() {}, getAliasDeletionJob() {}, getLatestAliasDeletionJob() {},
    createAliasDeletionStorage() {}, ADMIN_BASE_PATH: "/admin", isAliasDeletionJobTerminal,
    createAliasDeletionController: (value) => { options = value; return {}; },
    clearAliasSelection: () => refreshes.push("clear"),
    loadAliases: async () => refreshes.push("aliases"),
    loadAccounts: async () => refreshes.push("accounts"),
    loadGroups: async () => refreshes.push("groups"),
  };
  Function(...Object.keys(dependencies), `${factory}; makeDeletionController();`)(...Object.values(dependencies));
  options.onChange({ job: { jobId: "job", status: "queued" } });
  options.onChange({ job: { jobId: "job", status: "running" } });
  options.onChange({ job: { jobId: "job", status: "running", processed: 0, failed: 0, waits: [{ attempt: 1 }] } });
  options.onChange({ job: { jobId: "job", status: "running", processed: 0, failed: 0, waits: [] } });
  assert.deepEqual(refreshes, ["clear"]);
  options.onChange({ job: { jobId: "job", status: "interrupted" } });
  options.onChange({ job: { jobId: "job", status: "interrupted" } });
  assert.deepEqual(refreshes, ["clear", "clear", "aliases", "accounts", "groups"]);
  const previousState = state.value;
  auth.state.username = "another";
  options.onChange({ job: { jobId: "old-owner-job", status: "completed" } });
  assert.equal(state.value, previousState);
});

test("waiting renders server retry times, attempts, IDs and fixed Chinese operations without final counts", async () => {
  const raw = {
    job_id: "waiting-job", status: "running", requested: 416, processed: 0,
    deleted: 0, failed: 0, results: [],
  };
  const retryAt = "2026-09-08T01:08:23Z";
  const waits = ["validate", "list", "deactivate", "delete"].map((operation, index) => ({
    account_id: index === 3 ? 0 : 12, alias_id: index === 0 ? 0 : 91 + index,
    operation, retry_at: retryAt, attempt: index % 3 + 1, max_attempts: 3,
    http_status: 429, service_code: "UPSTREAM_SECRET", raw_body: "RAW_SECRET",
    message: "<script>wait-secret</script>",
  }));
  const waiting = await renderProgress({ ...raw, waits });
  assert.match(waiting.text, /以下主号触发 Apple 限流，等待后继续/);
  assert.match(waiting.text, /执行中（主号限流等待）/);
  assert.match(waiting.text, /仅上述主号等待；其他未限流主号继续处理/);
  assert.match(waiting.text, /已处理 0 \/ 416； 成功删除 0；失败\/未执行 0/);
  assert.match(waiting.text, /主号 ID 12/);
  assert.match(waiting.text, /邮箱 ID 94/);
  assert.doesNotMatch(waiting.text, /主号 ID 0|邮箱 ID 0|其中未执行/);
  for (const label of ["校验", "获取邮箱列表", "停用邮箱", "删除邮箱"]) {
    assert.ok(waiting.text.includes(label));
  }
  for (const attempt of [1, 2, 3]) assert.ok(waiting.text.includes(`第 ${attempt} 次重试（最多 3 次）`));
  assert.ok(waiting.text.includes(`预计重试时间：${formatTime(retryAt, { seconds: true })}`));
  assert.doesNotMatch(waiting.html, /UPSTREAM_SECRET|RAW_SECRET|wait-secret|<script>/);
  for (const next of [raw, { ...raw, waits: [] }, { ...raw, status: "completed", waits }]) {
    const resumed = await renderProgress(next);
    assert.doesNotMatch(resumed.text, /触发 Apple 限流|主号限流等待|仅上述主号等待|预计重试时间/);
    assert.match(resumed.text, next.status === "completed" ? /已完成/ : /执行中/);
    assert.match(resumed.text, /已处理 0 \/ 416/);
  }
});

test("account-specific waits coexist with other accounts' completed results and ongoing progress", async () => {
  const raw = {
    job_id: "multi-account-job", status: "running", requested: 8, processed: 2,
    deleted: 2, failed: 0,
    results: [
      { id: 201, address: "account-2-first@example.invalid", deleted: true },
      { id: 301, address: "account-3-first@example.invalid", deleted: true },
    ],
    waits: [{
      account_id: 1, alias_id: 101, operation: "delete",
      retry_at: "2026-09-08T01:08:23Z", attempt: 1, max_attempts: 3,
    }],
  };
  const waiting = await renderProgress(raw);
  assert.match(waiting.text, /执行中（主号限流等待）/);
  assert.match(waiting.text, /已处理 2 \/ 8； 成功删除 2；失败\/未执行 0/);
  assert.match(waiting.text, /主号 ID 1 · 邮箱 ID 101/);
  assert.doesNotMatch(waiting.text, /主号 ID [23]/);
  assert.match(waiting.text, /仅上述主号等待；其他未限流主号继续处理/);
  assert.match(waiting.text, /account-2-first@example.invalid： 已删除/);
  assert.match(waiting.text, /account-3-first@example.invalid： 已删除/);

  const progressed = await renderProgress({
    ...raw, processed: 3, deleted: 3,
    results: [...raw.results, { id: 302, address: "account-3-next@example.invalid", deleted: true }],
  });
  assert.match(progressed.text, /已处理 3 \/ 8； 成功删除 3；失败\/未执行 0/);
  assert.match(progressed.text, /主号 ID 1 · 邮箱 ID 101/);
  assert.match(progressed.text, /account-3-next@example.invalid： 已删除/);

  const resumed = await renderProgress({ ...raw, waits: [] });
  assert.match(resumed.text, /执行中/);
  assert.match(resumed.text, /已处理 2 \/ 8； 成功删除 2；失败\/未执行 0/);
  assert.doesNotMatch(resumed.text, /主号限流等待|触发 Apple 限流|仅上述主号等待|预计重试时间/);
  assert.match(resumed.text, /account-2-first@example.invalid： 已删除/);
  assert.match(resumed.text, /account-3-first@example.invalid： 已删除/);
});

test("416 rate-limited results have one retention notice each in both failure previews and details", async () => {
  const results = Array.from({ length: 416 }, (_, index) => ({
    id: index + 1, address: `alias-${index + 1}@example.invalid`, deleted: false,
    code: "APPLE_RATE_LIMITED", message: "Apple 限流；本地记录已保留", local_retained: true,
  }));
  const { html, text } = await renderProgress({
    job_id: "limited-job", status: "completed", requested: 416, processed: 416,
    deleted: 0, failed: 416, results,
  });
  assert.match(text, /失败\/未执行 416/);
  const preview = html.match(/<div class="alias-deletion-progress__failures">([\s\S]*?)<\/div>\s*<details/)[1];
  const details = html.match(/<details[^>]*>([\s\S]*?)<\/details>/)[1];
  assert.equal(preview.match(/本地记录已保留/g).length, 5);
  assert.equal(details.match(/本地记录已保留/g).length, 416);
  assert.doesNotMatch(text, /本地记录已保留[；; ]+本地记录已保留/);
});

test("deferred is a failed subset while unknown results remain pending in both displays", async () => {
  const { html, text } = await renderProgress({
    job_id: "deferred-job", status: "completed", requested: 416, processed: 416,
    deleted: 0, failed: 416, deferred: 414,
    results: [
      { id: 1, deleted: false, code: "APPLE_BATCH_DEFERRED", message: "主号持续限流；本地记录已保留", local_retained: true },
      { id: 2, deleted: false, code: "UNKNOWN", local_retained: false },
      { id: 3, deleted: false, code: "UNKNOWN", message: "<img src=x onerror=alert(1)>；本地记录已保留", local_retained: false },
    ],
  });
  assert.match(text, /失败\/未执行 416/);
  assert.match(text, /其中未执行 414/);
  assert.equal(text.match(/未执行：主号持续限流；本地记录已保留/g).length, 2);
  assert.equal(text.match(/删除结果待确认；Apple \/ 本地状态待核对/g).length, 2);
  assert.equal(text.match(/本地记录已保留/g).length, 2);
  assert.doesNotMatch(text, /删除失败|APPLE_BATCH_DEFERRED/);
  assert.doesNotMatch(html, /<img|<script/);
  assert.match(html, /&lt;img/);
  const unknown = await renderProgress(null, { uncertain: true, operationId: "pending-job", error: { code: "UNKNOWN" } });
  assert.match(unknown.text, /结果待确认/);
  assert.doesNotMatch(unknown.text, /失败|已处理|成功删除|本地记录已保留/);
});

test("malformed wait metadata cannot hide the original job or introduce HTML and raw-body secrets", async () => {
  const { html, text } = await renderProgress({
    job_id: "known-job", status: "running", requested: 416, processed: 7,
    deleted: 6, failed: 1, deferred: 500, results: [],
    waits: [null, "RAW_SECRET", { account_id: { secret: "RAW_SECRET" }, raw_body: "RAW_SECRET" }, {
      account_id: 12, alias_id: 91, operation: "<img src=x onerror=alert(1)>",
      retry_at: "<script>RAW_SECRET</script>", attempt: 1, max_attempts: 3,
    }],
  });
  assert.match(text, /known-job/);
  assert.match(text, /已处理 7 \/ 416； 成功删除 6；失败\/未执行 1/);
  assert.doesNotMatch(html, /RAW_SECRET|<img|<script/);
  assert.doesNotMatch(text, /主号限流等待|预计重试时间|其中未执行/);
});
