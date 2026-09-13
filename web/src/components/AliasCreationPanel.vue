<template>
  <div class="creation-job-panel">
    <h2 id="batch-creation-title" class="creation-job-panel__title">批量创建隐私邮箱</h2>
    <span class="creation-job-panel__budget-short">5 次/小时 · 20 次/日</span>
    <div class="creation-job-panel__apple">
      <el-tag :type="appleAuthenticated ? 'success' : 'info'" title="新通道登录状态">新通道：{{ appleAuthenticated ? '已登录' : '未登录' }}</el-tag>
      <el-button v-if="!appleAuthenticated" :disabled="active || !webAuthenticated || appleLoading" @click="openLogin">登录 Apple Account</el-button>
      <el-button v-else :disabled="active || appleLoading" @click="clearApple">退出新通道</el-button>
    </div>
    <div class="creation-job-panel__controls">
      <el-input-number v-model="count" :min="1" :max="100" :controls="false" aria-label="创建数量" :disabled="active || starting" />
      <el-select v-model="channel" aria-label="创建通道" :disabled="active || starting">
        <el-option label="自动（初始选择通道）" value="auto" />
        <el-option label="Apple Account 新通道" value="apple_account" />
        <el-option label="iCloud Web 旧通道" value="icloud_web" />
      </el-select>
      <el-button v-if="active" :loading="stopping" @click="stop">停止后续创建</el-button>
      <el-button v-else type="primary" :loading="starting" :disabled="!ready || !accountEnabled || !webAuthenticated || appleLoading" @click="start">开始批量创建</el-button>
      <el-button v-if="!ready" :loading="loading" @click="load">重新读取状态</el-button>
    </div>
    <details class="creation-job-panel__budget-details">
      <summary>创建节奏与等待说明</summary>
      <p>每主号手动与自动创建共用本地预算：滚动 1 小时最多 5 次、24 小时最多 20 次，尝试至少间隔 10 分钟；失败及待确认尝试也计数。自动只选择本轮初始通道，不因限流切换。Apple 明确限流后暂停至少 24 小时。本地上限不是 Apple 官方配额保证。100 次至少需要 5 天预算窗口；任务生命周期为 7 天。</p>
    </details>
    <RequestAlert v-if="error && !appleVisible" :error="error" closable @close="error = null" />
    <div v-if="job && active" class="creation-job-panel__status" aria-live="polite">
      <span>{{ statusLabel }}：{{ job.completed }}/{{ job.target }}<template v-if="job.status === 'waiting' && job.next_run_at">，预计 {{ formatTime(job.next_run_at, { seconds: true }) }}</template></span>
      <span v-if="job.last_error">{{ jobErrorMessage(job.last_error) }}</span>
    </div>
    <div v-if="job && !active && job.status !== 'completed'" class="creation-job-panel__status" aria-live="polite">
      <span>{{ statusLabel }}</span><span v-if="job.last_error">{{ jobErrorMessage(job.last_error) }}</span>
    </div>
    <el-dialog v-model="appleVisible" title="登录 Apple Account 新通道" width="min(480px, calc(100vw - 28px))" :close-on-click-modal="false" :before-close="closeLogin">
      <RequestAlert v-if="error" :error="error" />
      <el-form v-if="appleStep === 'login'" label-position="top" :disabled="appleLoading" @submit.prevent="loginApple">
        <el-form-item label="Apple ID"><el-input v-model="appleId" autocomplete="username" /></el-form-item>
        <el-form-item label="Apple 账户密码"><el-input v-model="applePassword" type="password" autocomplete="current-password" show-password /></el-form-item>
        <el-form-item label="账户区域"><el-select v-model="region"><el-option label="全球" value="global" /><el-option label="中国大陆" value="cn" /></el-select></el-form-item>
        <p class="field-help">密码仅用于本次登录。完成后会保存独立的新通道会话。</p>
      </el-form>
      <el-form v-else label-position="top" :disabled="appleLoading" @submit.prevent="verifyApple">
        <p class="field-help">在受信任的 Apple 设备上允许登录，并填写六位验证码。</p>
        <el-form-item label="验证码"><el-input v-model="code" maxlength="6" inputmode="numeric" autocomplete="one-time-code" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button :disabled="appleLoading" @click="closeLogin(() => { appleVisible = false; })">取消</el-button>
        <el-button type="primary" :loading="appleLoading" @click="appleStep === 'login' ? loginApple() : verifyApple()">{{ appleStep === 'login' ? '登录新通道' : '验证并连接' }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { createAliasCreationJob, getAliasCreationJob, stopAliasCreationJob, getAppleAccountAuth, loginAppleAccountAuth, verifyAppleAccountAuth, deleteAppleAccountAuth } from "../api/admin.js";
import RequestAlert from "./RequestAlert.vue";
import { formatTime } from "../utils/format.js";

const props = defineProps({ accountId: { type: [Number, String], required: true }, appleIdHint: { type: String, default: "" }, accountEnabled: Boolean, webAuthenticated: Boolean, csrfToken: { type: String, default: "" } });
const emit = defineEmits(["busy", "change"]);
const count = ref(5), channel = ref("auto"), job = ref(null);
const starting = ref(false), stopping = ref(false), loading = ref(false), ready = ref(false), error = ref(null);
const appleVisible = ref(false), appleLoading = ref(false), appleStep = ref("login"), appleSession = ref(null);
const appleId = ref(""), applePassword = ref(""), region = ref("global"), code = ref(""), challengeId = ref("");
let timer, alive = true, generation = 0, jobRequest = 0, sessionRequest = 0;
const jobStatusPollIntervalMs = 10_000;
const active = computed(() => ["running", "waiting"].includes(job.value?.status));
const appleAuthenticated = computed(() => appleSession.value?.status === "authenticated");
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
function isLocalBudgetWait(value) {
  const message = String(value || "");
  return message.toUpperCase().includes("APPLE_CREATION_BUDGET_WAIT") || message.includes("本地主号创建预算已用尽");
}
function isAppleRateLimited(value) {
  const message = String(value || "");
  return message.toUpperCase().includes("APPLE_RATE_LIMITED") || message.includes("Apple 请求被限流") || message.includes("Apple 请求过于频繁") || message.includes("Apple 返回了限流");
}
watch(() => active.value || starting.value || appleLoading.value, busy => emit("busy", busy));
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
  const current = guard(), request = ++jobRequest, session = ++sessionRequest;
  loading.value = true;
  try {
    const next = await getAliasCreationJob(props.accountId);
    if (!current()) return;
    if (request === jobRequest) { job.value = next; ready.value = true; }
    const result = await getAppleAccountAuth(props.accountId);
    if (current() && session === sessionRequest) appleSession.value = result.appleSession;
  } catch (e) { if (current()) error.value = e; }
  finally { if (current()) { loading.value = false; schedule(); } }
}
async function start() {
  if (starting.value || active.value || !ready.value) return;
  const n = Number(count.value);
  if (!Number.isInteger(n) || n < 1 || n > 100) { error.value = new Error("数量应为 1-100 的整数"); return; }
  if (channel.value === "apple_account" && !appleAuthenticated.value) { openLogin(); return; }
  const current = guard(); ++jobRequest; clearTimeout(timer); starting.value = true; error.value = null;
  try { const next = await createAliasCreationJob(props.accountId, { count: n, channel: channel.value }, props.csrfToken); if (current()) job.value = next; }
  catch (e) { if (current()) { error.value = e; await poll(); } }
  finally { if (current()) { starting.value = false; schedule(); } }
}
async function stop() {
  if (!active.value || stopping.value) return;
  const current = guard(); ++jobRequest; clearTimeout(timer); stopping.value = true; error.value = null;
  try { const next = await stopAliasCreationJob(props.accountId, props.csrfToken); if (current()) job.value = next; }
  catch (e) { if (current()) { error.value = e; stopping.value = false; } }
  if (current()) schedule();
}
function openLogin() { error.value = null; appleId.value = props.appleIdHint; applePassword.value = ""; code.value = ""; challengeId.value = ""; appleStep.value = "login"; appleVisible.value = true; }
function closeLogin(done) { if (appleLoading.value) return; applePassword.value = ""; code.value = ""; done(); }
async function loginApple() {
  if (appleLoading.value) return;
  if (!appleId.value.trim() || !applePassword.value) { error.value = new Error("请填写 Apple ID 和账户密码"); return; }
  const current = guard(); ++sessionRequest; appleLoading.value = true; error.value = null;
  try {
    const result = await loginAppleAccountAuth(props.accountId, { apple_id: appleId.value.trim(), password: applePassword.value, region: region.value }, props.csrfToken);
    if (!current()) return; appleSession.value = result.appleSession; challengeId.value = result.challengeId;
    if (result.status === "verification_required") appleStep.value = "verification"; else appleVisible.value = false;
  } catch (e) { if (current()) error.value = e; }
  finally { if (current()) { applePassword.value = ""; appleLoading.value = false; } }
}
async function verifyApple() {
  if (appleLoading.value) return;
  if (!/^\d{6}$/.test(code.value)) { error.value = new Error("请填写六位数字验证码"); return; }
  const current = guard(); ++sessionRequest; appleLoading.value = true; error.value = null;
  try {
    const result = await verifyAppleAccountAuth(props.accountId, { challenge_id: challengeId.value, code: code.value }, props.csrfToken);
    if (!current()) return; appleSession.value = result.appleSession; appleVisible.value = false; appleStep.value = "login";
  } catch (e) { if (current()) error.value = e; }
  finally { if (current()) { appleLoading.value = false; code.value = ""; } }
}
async function clearApple() {
  if (active.value || appleLoading.value) return;
  const current = guard(); ++sessionRequest; appleLoading.value = true; error.value = null;
  try { await deleteAppleAccountAuth(props.accountId, props.csrfToken); if (current()) appleSession.value = null; }
  catch (e) { if (current()) error.value = e; }
  finally { if (current()) appleLoading.value = false; }
}
watch(() => props.accountId, () => {
  generation++; clearTimeout(timer); job.value = null; ready.value = false; appleSession.value = null;
  starting.value = false; stopping.value = false; appleLoading.value = false; appleVisible.value = false;
  applePassword.value = ""; code.value = ""; error.value = null; load();
}, { immediate: true });
watch(() => props.webAuthenticated, () => load());
onBeforeUnmount(() => { alive = false; generation++; clearTimeout(timer); applePassword.value = ""; code.value = ""; emit("busy", false); });
</script>

<style scoped>
.creation-job-panel { display: flex; flex-wrap: wrap; gap: 8px 12px; align-items: center; min-width: 0; }
.creation-job-panel p { margin: 0; }
.creation-job-panel__controls, .creation-job-panel__apple { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.creation-job-panel__title { margin: 0; color: var(--text); font-size: 18px; line-height: 1.3; white-space: nowrap; }
.creation-job-panel__title { order: 1; }
.creation-job-panel__budget-short { order: 1; color: var(--text-secondary); font-size: 12px; white-space: nowrap; }
.creation-job-panel__controls { order: 2; margin-left: auto; }
.creation-job-panel__apple { order: 3; }
.creation-job-panel__budget-details { order: 4; color: var(--text-secondary); font-size: 13px; line-height: 1.5; }
.creation-job-panel__budget-details summary { cursor: pointer; }
.creation-job-panel__budget-details p { max-width: 720px; padding-top: 6px; }
.creation-job-panel__status { order: 5; }
.creation-job-panel > .el-alert { order: 6; }
.creation-job-panel__controls .el-select { width: 220px; }
.creation-job-panel__controls :deep(.el-input-number) { width: 104px; }
.creation-job-panel__status { display: flex; flex-basis: 100%; flex-wrap: wrap; gap: 8px; overflow-wrap: anywhere; color: var(--text-secondary); }
@media (max-width: 720px) { .creation-job-panel__controls { margin-left: 0; } .creation-job-panel__controls .el-select { width: 100%; } }
</style>
