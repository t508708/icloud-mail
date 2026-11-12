<template>
  <div class="apple-connection-panel">
    <el-tag :type="session?.health === 'retry_wait' ? 'warning' : accountAuthenticated ? 'success' : 'info'" effect="plain">新建连接：{{ statusLabel }}</el-tag>
    <el-tag :type="webAuthenticated ? 'success' : 'info'" effect="plain">目录连接：{{ webAuthenticated ? '已登录' : '未登录' }}</el-tag>
    <el-button class="apple-connection-panel__manage" :disabled="busy || loading" @click="openManager">管理 Apple 连接</el-button>
    <el-dialog v-model="visible" title="管理 Apple 连接" width="min(500px, calc(100vw - 28px))" :close-on-click-modal="false" :before-close="closeManager">
      <RequestAlert v-if="error" :error="error" closable @close="error = null" />
      <p class="field-help">新建优先使用 Apple Account；同步目录优先使用 iCloud Web，目录登录失效时可使用已登录的 Apple Account 读取。转发核对、停用和删除仍使用 iCloud Web。两套会话独立保存，分别验证，退出其中一个不会退出另一个。</p>
      <p class="field-help">登录仅配置连接，不会启用已停用的主号，也不会自动创建邮箱。</p>
      <template v-if="step === 'manage'">
        <section class="apple-connection-panel__channel">
          <h3>新建连接 · Apple Account</h3>
          <p class="field-help">{{ statusLabel }}<template v-if="session?.appleId"> · {{ session.appleId }}</template></p>
          <p v-if="session?.health === 'retry_wait'" class="field-help">续期暂未完成，已保留会话并安排重试<template v-if="session.nextRetryAt">：{{ connectionTime(session.nextRetryAt) }}</template>。只有 Apple 明确判定登录失效才需要重新登录。</p>
          <p v-if="session?.lastRefreshedAt" class="field-help">最近续期：{{ connectionTime(session.lastRefreshedAt) }}<template v-if="session.nextRefreshAt"> · 下次计划：{{ connectionTime(session.nextRefreshAt) }}</template></p>
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
        <p class="field-help">{{ verificationHint() }}</p>
        <template v-if="verification">
          <el-alert v-if="verification.delivery_warning" :title="verification.delivery_warning" type="warning" :closable="false" show-icon />
          <el-form-item label="验证码接收方式">
            <el-select v-model="verificationMethod" style="width:100%">
              <el-option v-if="verification.phones?.length" label="手机号短信" value="sms" />
              <el-option label="受信任的 Apple 设备" value="device" />
            </el-select>
          </el-form-item>
          <el-form-item v-if="verificationMethod === 'sms'" label="受信任手机号">
            <el-select v-model="verificationPhoneId" style="width:100%">
              <el-option v-for="phone in verification.phones" :key="phone.id" :label="phone.label" :value="phone.id" />
            </el-select>
          </el-form-item>
          <el-button v-if="!(verificationMethod === 'sms' && verification.sms_hidden)" :loading="loading" @click="requestCode">
            {{ verificationMethod === 'sms' ? '发送短信验证码' : '请求设备验证码' }}
          </el-button>
          <p class="field-help">已经收到验证码可直接填写，不必重发；每次发送至少间隔 60 秒，Apple 要求更久时继续等待。</p>
        </template>
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
import { getAppleAccountAuth, loginAppleAccountAuth, verifyAppleAccountAuth, deleteAppleAccountAuth, requestAppleAccountVerificationCode } from "../api/admin.js";
import { ElMessage } from "element-plus";
import RequestAlert from "./RequestAlert.vue";

const props = defineProps({ accountId: { type: [Number, String], required: true }, appleIdHint: { type: String, default: "" }, webAuthenticated: Boolean, csrfToken: { type: String, default: "" }, busy: Boolean });
const emit = defineEmits(["busy", "session-change", "open-web-login", "disconnect-web"]);
const visible = ref(false), loading = ref(false), error = ref(null), session = ref(null), step = ref("manage");
const appleId = ref(""), password = ref(""), region = ref("global"), code = ref(""), challengeId = ref("");
const verification = ref(null), verificationMethod = ref("device"), verificationPhoneId = ref(null);
const accountAuthenticated = computed(() => session.value?.status === "authenticated");
const statusLabel = computed(() => session.value?.health === "retry_wait" ? "等待续期重试" : ({ authenticated: "已登录", expired: "需重新登录", verification_required: "待验证" }[session.value?.status] || "未登录"));
function connectionTime(value) { const date = new Date(value); return Number.isNaN(date.getTime()) ? "待确认" : date.toLocaleString("zh-CN", { hour12: false }); }
const blocked = computed(() => props.busy || loading.value);
let alive = true, generation = 0, sessionRequest = 0;
function guard() { const g = generation, id = props.accountId; return () => alive && g === generation && id === props.accountId; }
function publish() { emit("session-change", session.value); }
function clearSecrets() { password.value = ""; code.value = ""; challengeId.value = ""; verification.value = null; }
function applyVerification(info) {
  verification.value = info || null;
  verificationMethod.value = info?.method || (info?.phones?.length ? "sms" : "device");
  verificationPhoneId.value = info?.method === "sms" ? info.phone_id : (info?.phones?.[0]?.id ?? null);
}
function verificationHint() {
  const info = verification.value;
  if (!info) return "在受信任的 Apple 设备上允许登录，并填写六位验证码。";
  if (info.method === "sms" && info.sent) return "Apple 已接受短信请求，请填写对应手机号收到的验证码。";
  if (info.method === "device" && info.sent) return "Apple 已接受设备验证码请求，请查看受信任的 Apple 设备。";
  if (info.phones?.length) return "请选择短信或设备验证；若已经收到短信，选择对应手机号后直接填写即可。";
  return "Apple 本次未提供可识别的短信手机号，请填写当前登录收到的设备验证码。";
}
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
      applyVerification(result.verification);
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
    const payload = { challenge_id: challengeId.value, code: code.value };
    if (verification.value) {
      payload.verification_method = verificationMethod.value;
      if (verificationMethod.value === "sms") payload.phone_id = verificationPhoneId.value;
    }
    const result = await verifyAppleAccountAuth(props.accountId, payload, props.csrfToken);
    if (!current()) return;
    if (result.status !== "authenticated") throw new Error("登录验证尚未完成，请重试。");
    session.value = result.appleSession; publish(); clearSecrets(); step.value = "manage";
    ElMessage.success("新建连接已登录");
  } catch (e) { if (current()) error.value = e; }
  finally { if (current()) { code.value = ""; loading.value = false; } }
}
async function requestCode() {
  if (blocked.value || !challengeId.value) return;
  const current = guard(); ++sessionRequest; loading.value = true; error.value = null; code.value = "";
  try {
    const result = await requestAppleAccountVerificationCode(props.accountId, {
      challenge_id: challengeId.value, method: verificationMethod.value,
      phone_id: verificationMethod.value === "sms" ? verificationPhoneId.value : 0,
    }, props.csrfToken);
    if (current()) applyVerification(result.verification);
  } catch (e) { if (current()) error.value = e; }
  finally { if (current()) loading.value = false; }
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
