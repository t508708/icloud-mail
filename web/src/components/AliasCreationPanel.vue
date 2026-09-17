<template>
  <div class="creation-job-panel">
    <h2 id="batch-creation-title" class="creation-job-panel__title">批量创建隐私邮箱</h2>
    <span class="creation-job-panel__budget-short">手动创建：不使用本地额度 · 成功后立即继续</span>
    <div class="creation-job-panel__controls">
      <el-input-number v-model="count" :min="1" :max="100" :controls="false" aria-label="创建数量" :disabled="active || starting" />
      <el-button :loading="probing" :disabled="probing || starting || job?.status === 'running' || !ready || !accountEnabled || !webAuthenticated || connectionBusy" @click="probe">手动探测 1 个</el-button>
      <el-button v-if="active" :loading="stopping" @click="stop">停止后续创建</el-button>
      <el-button v-else type="primary" :loading="starting" :disabled="probing || !ready || !accountEnabled || !webAuthenticated || connectionBusy" @click="start">开始批量创建</el-button>
      <el-button v-if="!ready" :loading="loading" @click="load">重新读取状态</el-button>
    </div>
    <span v-if="!webAuthenticated" class="creation-job-panel__connection-help">请在“管理 Apple 连接”中补充目录登录，用于核对转发与创建结果。</span>
    <RequestAlert v-if="probeError" :error="probeError" :message="probeErrorMessage(probeError)" :type="probeError.code === 'APPLE_RATE_LIMITED' ? 'info' : 'error'" closable @close="probeError = null" />
    <details class="creation-job-panel__budget-details">
      <summary>创建节奏与等待说明</summary>
      <p>手动批量创建和单次探测均不使用本地创建额度，成功后不额外等待；仍受 Apple 实际响应影响。自动计划按滚动 1 小时最多 18 次、尝试至少间隔 2 分钟执行，自动只选择本轮初始通道，不因限流切换。本地上限不是 Apple 官方配额保证；任务生命周期为 7 天。</p>
    </details>
    <RequestAlert v-if="error" :error="error" closable @close="error = null" />
    <div v-if="job && active" class="creation-job-panel__status" aria-live="polite">
      <span>{{ statusLabel }}：{{ job.completed }}/{{ job.target }}<template v-if="job.status === 'waiting' && job.next_run_at">，预计 {{ formatTime(job.next_run_at, { seconds: true }) }}</template></span>
      <span v-if="job.last_error">{{ jobErrorMessage(job.last_error) }}</span>
    </div>
    <div v-if="job && !active && job.status !== 'completed'" class="creation-job-panel__status" aria-live="polite">
      <span>{{ statusLabel }}</span><span v-if="job.last_error">{{ jobErrorMessage(job.last_error) }}</span>
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { createAliasCreationJob, createAliasNow, getAliasCreationJob, stopAliasCreationJob } from "../api/admin.js";
import { ElMessage } from "element-plus";
import RequestAlert from "./RequestAlert.vue";
import { formatTime } from "../utils/format.js";

const props = defineProps({ accountId: { type: [Number, String], required: true }, accountEnabled: Boolean, webAuthenticated: Boolean, connectionBusy: Boolean, csrfToken: { type: String, default: "" } });
const emit = defineEmits(["busy", "operation-busy", "change"]);
const count = ref(5), job = ref(null);
const starting = ref(false), stopping = ref(false), loading = ref(false), ready = ref(false), error = ref(null);
const probing = ref(false), probeError = ref(null);
let timer, alive = true, generation = 0, jobRequest = 0;
const jobStatusPollIntervalMs = 10_000;
const active = computed(() => ["running", "waiting"].includes(job.value?.status));
const statusLabel = computed(() => {
  if (job.value?.status === "waiting") {
    if (isLocalBudgetWait(job.value?.last_error)) return "等待本地主号共享预算恢复";
    if (isAppleRateLimited(job.value?.last_error)) return "Apple 限流暂停中（至少 24 小时）";
    return "等待任务继续";
  }
  return ({ running: "创建中", completed: "已完成", stopped: "已停止", failed: "本轮已停止", interrupted: "已中断" }[job.value?.status] || "任务");
});
function jobErrorMessage(value) {
  if (isLocalBudgetWait(value)) return "本地主号创建预算已用尽，本地等待不表示 Apple 返回了限流。";
  if (isAppleRateLimited(value)) return "Apple 返回限流；该通道暂停至少 24 小时，不会自动切换通道。";
  return value;
}
function probeErrorMessage(error) {
  const message = String(error?.message || error || "");
  if (error?.code === "APPLE_RATE_LIMITED") return "本次手动探测仍被 Apple 限流；后台冷却与计划保持不变，本次不自动重试。";
  return message;
}
function isLocalBudgetWait(value) {
  const message = String(value || "");
  return message.toUpperCase().includes("APPLE_CREATION_BUDGET_WAIT") || message.includes("本地主号创建预算已用尽");
}
function isAppleRateLimited(value) {
  const message = String(value || "");
  return message.toUpperCase().includes("APPLE_RATE_LIMITED") || message.includes("Apple 请求被限流") || message.includes("Apple 请求过于频繁") || message.includes("Apple 返回了限流");
}
watch(() => active.value || starting.value || probing.value, busy => emit("busy", busy));
watch(() => job.value?.status === "running" || starting.value || probing.value, busy => emit("operation-busy", busy));
watch(() => `${job.value?.id}:${job.value?.completed}:${job.value?.status}`, () => {
  if (!active.value) stopping.value = false;
  if (job.value) emit("change", job.value);
});
function guard() { const g = generation, id = props.accountId; return () => alive && g === generation && id === props.accountId; }
function schedule() { clearTimeout(timer); if (alive && (active.value || !ready.value)) timer = setTimeout(poll, jobStatusPollIntervalMs); }
async function poll() {
  const valid = guard(), request = ++jobRequest;
  const current = () => valid() && request === jobRequest;
  try { const next = await getAliasCreationJob(props.accountId); if (!current()) return; job.value = next; ready.value = true; }
  catch (e) { if (!current()) return; error.value = e; ready.value = false; }
  if (current()) schedule();
}
async function load() {
  const current = guard(), request = ++jobRequest;
  loading.value = true;
  try {
    const next = await getAliasCreationJob(props.accountId);
    if (!current()) return;
    if (request === jobRequest) { job.value = next; ready.value = true; }
  } catch (e) { if (current()) error.value = e; }
  finally { if (current()) { loading.value = false; schedule(); } }
}
async function start() {
  if (starting.value || probing.value || active.value || !ready.value || !props.accountEnabled || !props.webAuthenticated || props.connectionBusy) return;
  const n = Number(count.value);
  if (!Number.isInteger(n) || n < 1 || n > 100) { error.value = new Error("数量应为 1-100 的整数"); return; }
  const current = guard(); ++jobRequest; clearTimeout(timer); starting.value = true; error.value = null;
  try { const next = await createAliasCreationJob(props.accountId, { count: n, channel: "auto" }, props.csrfToken); if (current()) job.value = next; }
  catch (e) { if (current()) { error.value = e; await poll(); } }
  finally { if (current()) { starting.value = false; schedule(); } }
}
async function probe() {
  if (probing.value || starting.value || !ready.value || job.value?.status === "running" || !props.accountEnabled || !props.webAuthenticated || props.connectionBusy) return;
  const current = guard(); probing.value = true; probeError.value = null;
  try {
    await createAliasNow(props.accountId, props.csrfToken, "auto");
    if (!current()) return; emit("change", { account_id: props.accountId }); ElMessage.success("手动探测成功，邮箱已加入列表");
  } catch (e) {
    if (current()) {
      probeError.value = e;
      if (e?.code === "APPLE_ALIAS_CONFIRMATION_PENDING" || e?.code === "ALIAS_CREATED_DETAILS_PENDING") emit("change", { account_id: props.accountId });
    }
  }
  finally { if (current()) probing.value = false; }
}
async function stop() {
  if (!active.value || stopping.value) return;
  const current = guard(); ++jobRequest; clearTimeout(timer); stopping.value = true; error.value = null;
  try { const next = await stopAliasCreationJob(props.accountId, props.csrfToken); if (current()) job.value = next; }
  catch (e) { if (current()) { error.value = e; stopping.value = false; } }
  if (current()) schedule();
}
watch(() => props.accountId, () => {
  generation++; clearTimeout(timer); job.value = null; ready.value = false;
  starting.value = false; stopping.value = false;
  probing.value = false; probeError.value = null;
  error.value = null; load();
}, { immediate: true });
watch(() => props.webAuthenticated, () => load());
onBeforeUnmount(() => { alive = false; generation++; clearTimeout(timer); emit("busy", false); emit("operation-busy", false); });
</script>

<style scoped>
.creation-job-panel { display: flex; flex-wrap: wrap; gap: 8px 12px; align-items: center; min-width: 0; }
.creation-job-panel p { margin: 0; }
.creation-job-panel__controls { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.creation-job-panel__title { margin: 0; color: var(--text); font-size: 18px; line-height: 1.3; white-space: nowrap; }
.creation-job-panel__title { order: 1; }
.creation-job-panel__budget-short { order: 1; color: var(--text-secondary); font-size: 12px; white-space: nowrap; }
.creation-job-panel__controls { order: 2; margin-left: auto; }
.creation-job-panel__connection-help { order: 3; color: var(--text-secondary); font-size: 13px; }
.creation-job-panel__budget-details { order: 4; color: var(--text-secondary); font-size: 13px; line-height: 1.5; }
.creation-job-panel__budget-details summary { cursor: pointer; }
.creation-job-panel__budget-details p { max-width: 720px; padding-top: 6px; }
.creation-job-panel__status { order: 5; }
.creation-job-panel > .el-alert { order: 6; }
.creation-job-panel__controls :deep(.el-input-number) { width: 104px; }
.creation-job-panel__status { display: flex; flex-basis: 100%; flex-wrap: wrap; gap: 8px; overflow-wrap: anywhere; color: var(--text-secondary); }
@media (max-width: 720px) { .creation-job-panel__controls { margin-left: 0; } }
</style>
