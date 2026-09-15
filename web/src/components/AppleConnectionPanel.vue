<template>
  <div class="apple-connection-panel">
    <el-tag :type="accountAuthenticated ? 'success' : 'info'" effect="plain">新建连接：{{ statusLabel }}</el-tag>
    <el-tag :type="webAuthenticated ? 'success' : 'info'" effect="plain">目录连接：{{ webAuthenticated ? '已登录' : '未登录' }}</el-tag>
    <el-button class="apple-connection-panel__manage" :disabled="busy || loading" @click="openManager">管理 Apple 连接</el-button>
    <el-dialog v-model="visible" title="管理 Apple 连接" width="min(500px, calc(100vw - 28px))" :close-on-click-modal="false" :before-close="closeManager">
      <RequestAlert v-if="error" :error="error" closable @close="error = null" />
      <p class="field-help">新建优先使用 Apple Account；目录同步、转发核对、停用和删除使用 iCloud Web。两套会话独立保存，分别验证，退出其中一个不会退出另一个。</p>
      <p class="field-help">登录仅配置连接，不会启用已停用的主号，也不会自动创建邮箱。</p>
      <template v-if="step === 'manage'">
        <section class="apple-connection-panel__channel">
          <h3>新建连接 · Apple Account</h3>
          <p class="field-help">{{ statusLabel }}<template v-if="session?.appleId"> · {{ session.appleId }}</template></p>
          <div class="apple-connection-panel__actions">
            <el-button type="primary" :disabled="blocked" @click="openLogin">{{ accountAuthenticated ? '重新登录新建连接' : '登录新建连接' }}</el-button>
            <el-button v-if="accountAuthenticated" :disabled="blocked" @click="logoutAccount">退出新建连接</el-button>
          </div>
        </section>
        <section class="apple-connection-panel__channel">
          <h3>目录连接 · iCloud Web</h3>
          <p class="field-help">{{ webAuthenticated ? '已登录，可同步与管理隐藏邮箱目录。' : '尚未登录；创建前也需要此连接核对转发目标与结果。' }}</p>
          <div class="apple-connection-panel__actions">
            <el-button :disabled="blocked" @click="manageWeb('open-web-login')">{{ webAuthenticated ? '重新登录目录连接' : '登录目录连接' }}</el-button>
            <el-button v-if="webAuthenticated" :disabled="blocked" @click="manageWeb('disconnect-web')">退出目录连接</el-button>
          </div>
        </section>
      </template>
      <el-form v-else-if="step === 'login'" label-position="top" :disabled="blocked" @submit.prevent="login">
        <el-form-item label="Apple ID"><el-input v-model="appleId" autocomplete="username" /></el-form-item>
        <el-form-item label="Apple 账户密码"><el-input v-model="password" type="password" show-password autocomplete="current-password" /></el-form-item>
        <el-form-item label="账户区域"><el-select v-model="region"><el-option label="全球" value="global" /><el-option label="中国大陆" value="cn" /></el-select></el-form-item>
        <p class="field-help">密码仅用于本次登录，完成后保存加密会话。</p>
      </el-form>
      <el-form v-else label-position="top" :disabled="blocked" @submit.prevent="verify">
        <p class="field-help">在受信任的 Apple 设备上允许登录，并填写六位验证码。</p>
        <el-form-item label="验证码"><el-input v-model="code" maxlength="6" inputmode="numeric" autocomplete="one-time-code" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button :disabled="loading" @click="step === 'manage' ? closeManager(() => { visible = false; }) : backToManager()">{{ step === 'manage' ? '关闭' : '返回连接管理' }}</el-button>
        <el-button v-if="step !== 'manage'" type="primary" :disabled="busy" :loading="loading" @click="step === 'login' ? login() : verify()">{{ step === 'login' ? '登录新建连接' : '验证并连接' }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { getAppleAccountAuth, loginAppleAccountAuth, verifyAppleAccountAuth, deleteAppleAccountAuth } from "../api/admin.js";
import { ElMessage } from "element-plus";
import RequestAlert from "./RequestAlert.vue";

const props = defineProps({ accountId: { type: [Number, String], required: true }, appleIdHint: { type: String, default: "" }, webAuthenticated: Boolean, csrfToken: { type: String, default: "" }, busy: Boolean });
const emit = defineEmits(["busy", "session-change", "open-web-login", "disconnect-web"]);
const visible = ref(false), loading = ref(false), error = ref(null), session = ref(null), step = ref("manage");
const appleId = ref(""), password = ref(""), region = ref("global"), code = ref(""), challengeId = ref("");
const accountAuthenticated = computed(() => session.value?.status === "authenticated");
const statusLabel = computed(() => ({ authenticated: "已登录", expired: "需重新登录", verification_required: "待验证" }[session.value?.status] || "未登录"));
const blocked = computed(() => props.busy || loading.value);
let alive = true, generation = 0, sessionRequest = 0;
function guard() { const g = generation, id = props.accountId; return () => alive && g === generation && id === props.accountId; }
function publish() { emit("session-change", session.value); }
function clearSecrets() { password.value = ""; code.value = ""; challengeId.value = ""; }
watch(loading, busy => emit("busy", busy), { flush: "sync" });

async function load() {
  const valid = guard(), request = ++sessionRequest;
  try {
    const result = await getAppleAccountAuth(props.accountId);
    if (!valid() || request !== sessionRequest) return;
    session.value = result.appleSession; publish();
  } catch (e) { if (valid() && request === sessionRequest) error.value = e; }
}
function openManager() {
  if (blocked.value) return;
  clearSecrets(); step.value = "manage"; visible.value = true;
  load();
}
function closeManager(done) {
  if (loading.value) return;
  clearSecrets(); step.value = "manage"; done();
}
function backToManager() {
  if (loading.value) return;
  clearSecrets(); error.value = null; step.value = "manage";
}
function openLogin() {
  if (blocked.value) return;
  ++sessionRequest; clearSecrets(); error.value = null;
  appleId.value = session.value?.appleId || props.appleIdHint;
  region.value = session.value?.region || "global";
  step.value = "login";
}
function manageWeb(event) {
  if (blocked.value) return;
  clearSecrets(); visible.value = false; emit(event);
}
async function login() {
  if (blocked.value) return;
  if (!appleId.value.trim() || !password.value) { error.value = new Error("请填写 Apple ID 和账户密码"); return; }
  const current = guard(); ++sessionRequest; loading.value = true; error.value = null;
  try {
    const result = await loginAppleAccountAuth(props.accountId, { apple_id: appleId.value.trim(), password: password.value, region: region.value }, props.csrfToken);
    if (!current()) return;
    if (result.status === "verification_required") {
      if (!result.challengeId) throw new Error("登录未返回验证标识，请重新登录。");
      challengeId.value = result.challengeId; step.value = "verification";
    } else if (result.status === "authenticated") {
      step.value = "manage"; clearSecrets(); ElMessage.success("新建连接已登录");
    } else throw new Error("登录状态未确认，请重新登录。");
    session.value = result.appleSession; publish();
  } catch (e) { if (current()) error.value = e; }
  finally { if (current()) { password.value = ""; loading.value = false; } }
}
async function verify() {
  if (blocked.value) return;
  if (!/^\d{6}$/.test(code.value)) { error.value = new Error("请填写六位数字验证码"); return; }
  const current = guard(); ++sessionRequest; loading.value = true; error.value = null;
  try {
    const result = await verifyAppleAccountAuth(props.accountId, { challenge_id: challengeId.value, code: code.value }, props.csrfToken);
    if (!current()) return;
    if (result.status !== "authenticated") throw new Error("登录验证尚未完成，请重试。");
    session.value = result.appleSession; publish(); clearSecrets(); step.value = "manage";
    ElMessage.success("新建连接已登录");
  } catch (e) { if (current()) error.value = e; }
  finally { if (current()) { code.value = ""; loading.value = false; } }
}
async function logoutAccount() {
  if (blocked.value) return;
  const current = guard(); ++sessionRequest; loading.value = true; error.value = null;
  try {
    await deleteAppleAccountAuth(props.accountId, props.csrfToken);
    if (current()) { session.value = null; clearSecrets(); publish(); ElMessage.success("新建连接已退出，目录连接保持不变"); }
  } catch (e) { if (current()) error.value = e; }
  finally { if (current()) loading.value = false; }
}
watch(() => props.accountId, () => {
  generation++; ++sessionRequest; clearSecrets(); visible.value = false;
  step.value = "manage"; loading.value = false; session.value = null; error.value = null;
  publish(); load();
}, { immediate: true });
onBeforeUnmount(() => { alive = false; generation++; clearSecrets(); emit("busy", false); });
</script>

<style scoped>
.apple-connection-panel { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; min-width: 0; }
.apple-connection-panel__manage { margin-left: auto; }
.apple-connection-panel__channel { margin-top: 20px; }
.apple-connection-panel__channel h3 { color: var(--text); font-size: 15px; margin: 0 0 8px; }
.apple-connection-panel__actions { display: flex; flex-wrap: wrap; gap: 8px; }
.apple-connection-panel__actions :deep(.el-button + .el-button) { margin-left: 0; }
.apple-connection-panel .field-help { overflow-wrap: anywhere; }
</style>
