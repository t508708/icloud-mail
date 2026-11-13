<template>
  <section class="content-narrow page-stack account-form-page">
    <div v-if="loading" class="form-panel loading-panel" role="status" aria-live="polite" aria-busy="true" aria-label="正在加载主号表单">
      <el-skeleton :rows="7" animated />
    </div>

    <div v-else-if="loadError" class="load-failed">
      <RequestAlert :error="loadError" />
      <div class="button-row">
        <el-button :icon="Back" @click="cancel">返回</el-button>
        <el-button type="primary" :icon="Refresh" @click="loadAccount">
          重新加载
        </el-button>
      </div>
    </div>

    <el-form
      v-else
      ref="formRef"
      class="form-panel"
      :model="form"
      :rules="rules"
      label-position="top"
      :disabled="submitting"
      status-icon
      @submit.prevent="submit"
    >
      <RequestAlert
        v-if="submitError"
        :error="submitError"
        closable
        @close="submitError = null"
      />

      <div class="form-grid">
        <el-form-item label="备注" prop="name">
          <el-input
            v-model="form.name"
            maxlength="80"
            show-word-limit
            placeholder="例如：个人主号"
            autocomplete="off"
          />
        </el-form-item>

        <el-form-item label="邮箱类型" prop="mailboxType">
          <el-select
            v-model="form.mailboxType"
            style="width: 100%"
            :disabled="emailLocked"
          >
            <el-option label="iCloud 隐私邮箱" value="icloud" />
            <el-option label="自定义邮箱" value="custom" />
          </el-select>
          <p class="field-help">
            iCloud 规则和自动创建计划保持不变；自定义邮箱可按后缀批量生成地址。
          </p>
        </el-form-item>

        <div class="form-span mailbox-route-summary" role="status">
          <strong>当前收件规则：{{ receiveRuleLabel }}</strong>
          <p class="field-help">{{ receiveRuleHelp }}</p>
        </div>

        <el-form-item v-if="!isCustomMailbox" label="iCloud 主号邮箱" prop="email">
          <el-input
            v-model="form.email"
            type="email"
            placeholder="name@icloud.com"
            autocomplete="email"
            :readonly="emailLocked"
          />
          <p v-if="emailLocked" class="field-help">
            已有隐私邮箱后，主号邮箱不能修改。
          </p>
        </el-form-item>

        <el-form-item
          v-if="isCustomMailbox"
          label="邮箱后缀"
          prop="emailSuffix"
        >
          <el-input
            v-model="form.emailSuffix"
            placeholder="example.com 或 @example.com"
            autocomplete="off"
            :readonly="emailLocked"
          />
          <p class="field-help">
            随机邮箱会按 8–12 位英文数字 + @后缀生成，不受 iCloud 每小时规则限制。
          </p>
        </el-form-item>

        <el-form-item label="IMAP 用户名" prop="imapUsername">
          <el-input
            v-model="form.imapUsername"
            :placeholder="isCustomMailbox ? '填写邮箱服务器的登录用户名' : '通常填写完整 iCloud 邮箱'"
            autocomplete="username"
          />
          <p v-if="!isCustomMailbox" class="field-help">
            若 iCloud 隐私邮箱转发到第三方邮箱，请填写第三方 IMAP 登录/最终投递邮箱（例如 mango@example.com）；X-Original-To 可能仍是中间转发地址。
          </p>
        </el-form-item>

        <el-form-item class="form-span imap-service-field" label="IMAP 服务">
          <div class="imap-service-fields">
            <el-form-item prop="imapHost" label="主机" class="imap-service-field__host">
              <el-input
                v-model="form.imapHost"
                placeholder="imap.mail.me.com"
                autocomplete="off"
                aria-label="IMAP 主机"
              />
            </el-form-item>
            <el-form-item prop="imapPort" label="端口" class="imap-service-field__port">
              <el-input-number
                v-model="form.imapPort"
                :step="1"
                :controls="false"
                aria-label="IMAP 端口"
              />
            </el-form-item>
          </div>
          <p class="field-help">iCloud 默认使用 imap.mail.me.com:993。固定启用 TLS 1.2 或更高版本并校验证书，不自动切换端口。</p>
        </el-form-item>

        <el-form-item
          class="form-span"
          :label="usesGenericIMAPPassword ? 'IMAP 密码' : 'App 专用密码'"
          prop="imapPassword"
        >
          <el-input
            v-model="form.imapPassword"
            type="password"
            show-password
            :placeholder="isEdit ? '留空则保留当前密码' : usesGenericIMAPPassword ? '填写邮箱服务器的 IMAP 密码' : '在 Apple 账户中生成的专用密码'"
            autocomplete="new-password"
          />
          <p class="field-help">
            {{ usesGenericIMAPPassword ? "第三方/自定义邮箱的 IMAP 密码只会加密保存，后续不会回显。" : "Apple App 专用密码只会加密保存，后续不会回显。" }}
          </p>
          <p v-if="appPasswordShapeUnusual" class="field-help" role="status">当前输入与常见 App 专用密码格式不同。请确认使用此 Apple 账户生成的 16 位字母专用密码，可保留分组连字符；Apple 登录密码用于下方连接登录。</p>
        </el-form-item>

        <section class="form-span proxy-card" aria-labelledby="proxy-heading">
          <div class="switch-field">
            <div>
              <h2 id="proxy-heading">主号代理</h2>
              <p>支持 http、https、socks5；前置代理可选。</p>
            </div>
            <el-switch v-model="form.proxyEnabled" inline-prompt active-text="开启" inactive-text="关闭" aria-label="启用主号代理" />
          </div>
          <p v-if="!form.proxyEnabled" class="field-help" role="status">代理已关闭，主号连接将直接访问邮件服务器。</p>
          <template v-else>
            <div class="proxy-fields">
              <el-form-item label="代理地址" prop="proxyEndpoint" :required="!isEdit || !proxy.configured">
                <el-input v-model="form.proxyEndpoint" placeholder="http://user:password@proxy.example.com:8080" autocomplete="off" />
                <p v-if="proxy.configured" class="field-help">已保存代理地址；留空保留当前配置，输入新地址可覆盖。</p>
                <p v-else class="field-help">新建启用代理时必填，例如 socks5://127.0.0.1:1080。</p>
              </el-form-item>
              <el-form-item label="前置代理" prop="proxyUpstream">
                <el-input v-model="form.proxyUpstream" :disabled="form.clearProxyUpstream" placeholder="https://proxy.example.com:8443" autocomplete="off" />
                <p v-if="proxy.upstreamConfigured" class="field-help">已保存前置代理；留空保留当前配置。</p>
                <p v-else class="field-help">可选。留空表示不设置前置代理。</p>
              </el-form-item>
            </div>
            <el-checkbox v-if="isEdit && proxy.upstreamConfigured" v-model="form.clearProxyUpstream" @change="(checked) => { if (checked) form.proxyUpstream = ''; }">清除已保存的前置代理</el-checkbox>
            <div class="proxy-test-row">
              <el-button :loading="proxyTesting" @click="testProxy">测试代理</el-button>
              <span v-if="proxyTestResult" :class="proxyTestResult.ok ? 'proxy-test-success' : 'proxy-test-error'" role="status">{{ proxyTestResult.message }}</span>
            </div>
          </template>
        </section>

        <section v-if="!isCustomMailbox" class="form-span auto-login-card" aria-labelledby="auto-login-heading" :aria-busy="autoLoginLoading">
          <div class="switch-field">
            <div>
              <h2 id="auto-login-heading">自动保活服务</h2>
              <p>新通道或旧通道需要重新登录时，自动提交账户密码并从接码链接读取双重验证码，补充现有保活。</p>
            </div>
            <el-switch
              v-model="form.autoLoginEnabled"
              :disabled="!autoLoginReady || autoLoginLoading"
              inline-prompt
              active-text="开启"
              inactive-text="关闭"
              aria-label="自动保活服务"
            />
          </div>
          <p v-if="autoLoginLoading" class="field-help" role="status">正在读取自动保活配置…</p>
          <div v-else-if="autoLoginLoadError" class="auto-login-load-error" role="alert">
            <p>自动保活配置读取失败。为保留原有设置，请重新读取后再保存主号。</p>
            <el-button :icon="Refresh" @click="loadAutoLogin">重新读取保活配置</el-button>
          </div>
          <template v-else-if="autoLoginReady && form.autoLoginEnabled">
            <p class="field-help">这里填写 Apple 账户登录密码，与上方收件用的 App 专用密码不同。密码与接码链接加密保存，保存后不回显；仅在重新登录时使用。</p>
            <div class="auto-login-fields">
              <el-form-item label="Apple 账户区域" prop="autoLoginRegion" required>
                <el-select v-model="form.autoLoginRegion" placeholder="请选择账户区域" style="width: 100%">
                  <el-option label="国际区" value="global" />
                  <el-option label="中国大陆" value="cn" />
                </el-select>
              </el-form-item>
              <el-form-item label="Apple 账户登录密码" prop="autoLoginPassword" required>
                <el-input
                  v-model="form.autoLoginPassword"
                  type="password"
                  :placeholder="savedAutoLogin.passwordConfigured ? '已设置，留空保留，可覆盖' : '填写 Apple 账户登录密码'"
                  autocomplete="new-password"
                  :spellcheck="false"
                  autocapitalize="off"
                />
              </el-form-item>
              <el-form-item class="form-span" label="保活双重验证接码链接" prop="autoLoginCodeURL" required>
                <el-input
                  v-model="form.autoLoginCodeURL"
                  type="password"
                  :placeholder="savedAutoLogin.codeURLConfigured ? '已设置，留空保留，可覆盖' : '填写完整的 http 或 https 接码链接'"
                  autocomplete="new-password"
                  :spellcheck="false"
                  autocapitalize="off"
                />
                <p class="field-help">请选择该账户双重验证对应的接码链接。关闭自动保活后，这三项无需填写。</p>
              </el-form-item>
            </div>
          </template>
          <div v-if="autoLoginReady && savedAutoLogin.channels.length" class="auto-login-channels" aria-label="自动保活状态">
            <div v-for="(channel, index) in savedAutoLogin.channels" :key="`${channel.channel}:${index}`" class="auto-login-channel">
              <strong>{{ channel.channel === 'new' ? '新通道' : channel.channel === 'legacy' ? '旧通道' : '连接通道' }}</strong>
              <span>{{ autoLoginStatusLabel(channel.status) }}</span>
              <span v-if="channel.lastAttemptAt">最近尝试 {{ formatTime(channel.lastAttemptAt) }}</span>
              <span v-if="channel.lastSuccessAt">最近成功 {{ formatTime(channel.lastSuccessAt) }}</span>
              <span v-if="channel.nextAttemptAt">下次尝试 {{ formatTime(channel.nextAttemptAt) }}</span>
              <span v-if="channel.lastErrorCode">状态码 {{ channel.lastErrorCode }}</span>
            </div>
          </div>
        </section>

        <el-form-item v-if="isEdit" class="form-span account-enabled-field">
          <div class="switch-field">
            <div>
              <strong>启用主号同步</strong>
              <p>停用后隐藏邮箱列表不显示下属邮箱，邮箱池入池候选也隐藏；重新启用后恢复显示及原池状态，不删除邮箱或领取历史。</p>
            </div>
            <el-switch
              v-model="form.enabled"
              inline-prompt
              active-text="启用"
              inactive-text="停用"
              aria-label="启用主号同步"
            />
          </div>
        </el-form-item>
      </div>

      <div class="form-actions">
        <el-button :icon="Close" @click="cancel">取消</el-button>
        <el-button
          native-type="submit"
          type="primary"
          :icon="Check"
          :loading="submitting"
          :disabled="!isCustomMailbox && (!autoLoginReady || autoLoginLoading)"
        >
          保存
        </el-button>
      </div>
    </el-form>
  </section>
</template>

<script setup>
import { Back, Check, Close, Refresh } from "@element-plus/icons-vue";
import { ElMessageBox } from "element-plus";
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";

import {
  createAccount,
  getAccount,
  getAccountAutoLogin,
  testAccountProxy,
  updateAccount,
} from "../api/admin.js";
import RequestAlert from "../components/RequestAlert.vue";
import { useAuth } from "../stores/auth.js";
import {
  createActionLock,
  createLatestRequestGate,
} from "../utils/asyncState.js";
import { confirmationCancelled, successMessage } from "../utils/feedback.js";
import { formatTime } from "../utils/format.js";
import {
  autoLoginPayload,
  autoLoginStatusLabel,
  autoLoginValidation,
  clearAutoLoginSecrets,
} from "../utils/autoLogin.js";
import {
  DEFAULT_IMAP_HOST,
  DEFAULT_IMAP_PORT,
  mailboxReceiveRule,
  normalizeIMAPEndpoint,
  validateIMAPHost as validateIMAPHostValue,
  validateIMAPPort as validateIMAPPortValue,
} from "../utils/imap.js";

const route = useRoute();
const router = useRouter();
const auth = useAuth();
const formRef = ref(null);
const loading = ref(false);
const submitting = ref(false);
const loadError = ref(null);
const submitError = ref(null);
const aliasCount = ref(0);
const storedEnabled = ref(true);
const proxy = reactive({ configured: false, upstreamConfigured: false });
const proxyTesting = ref(false);
const proxyTestResult = ref(null);
const autoLoginReady = ref(false);
const autoLoginLoading = ref(false);
const autoLoginLoadError = ref(false);
const savedAutoLogin = reactive({ passwordConfigured: false, codeURLConfigured: false, channels: [] });
const loadGate = createLatestRequestGate();
const autoLoginGate = createLatestRequestGate();
const submitLock = createActionLock();
let viewActive = true;

const form = reactive({
  name: "",
  email: "",
  mailboxType: "icloud",
  emailSuffix: "",
  imapHost: DEFAULT_IMAP_HOST,
  imapPort: DEFAULT_IMAP_PORT,
  imapUsername: "",
  imapPassword: "",
  proxyEnabled: false,
  proxyEndpoint: "",
  proxyUpstream: "",
  clearProxyUpstream: false,
  enabled: true,
  autoLoginEnabled: false,
  autoLoginRegion: "global",
  autoLoginPassword: "",
  autoLoginCodeURL: "",
});

const isEdit = computed(() => route.name === "account-edit");
const isCustomMailbox = computed(() => form.mailboxType === "custom");
const emailLocked = computed(() => isEdit.value && aliasCount.value > 0);
const receiveRule = computed(() => mailboxReceiveRule(form));
const usesGenericIMAPPassword = computed(
  () => isCustomMailbox.value || receiveRule.value === "icloud-forwarded",
);
const appPasswordShapeUnusual = computed(() => !usesGenericIMAPPassword.value && Boolean(form.imapPassword.trim()) && !/^(?:[a-z]{16}|[a-z]{4}(?:-[a-z]{4}){3})$/i.test(form.imapPassword.trim()));
const receiveRuleLabel = computed(() => {
  switch (receiveRule.value) {
    case "custom":
      return "自定义域名邮箱";
    case "icloud-forwarded":
      return "iCloud 隐私邮箱 + 转发第三方 IMAP";
    default:
      return "iCloud 隐私邮箱 + iCloud IMAP";
  }
});
const receiveRuleHelp = computed(() => {
  switch (receiveRule.value) {
    case "custom":
      return "按原始投递收件人标头匹配自定义域名地址。";
    case "icloud-forwarded":
      return "保留 iCloud 隐私邮箱类型；系统会结合 X-ICLOUD-HME 和转发链路投递标头匹配地址。";
    default:
      return "使用 iCloud IMAP 的主号投递标头匹配隐私邮箱。";
  }
});

function routeKey(name = route.name, id = route.params.id) {
  return `${String(name || "")}:${String(id || "")}`;
}

function runeLength(value) {
  return Array.from(String(value || "")).length;
}

function validateEmail(_, value, callback) {
  const normalized = String(value || "").trim();
  if (!normalized) {
    if (isCustomMailbox.value) {
      callback();
      return;
    }
    callback(new Error("请填写 iCloud 主号邮箱"));
    return;
  }
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(normalized)) {
    callback(new Error("邮箱地址格式不正确"));
    return;
  }
  callback();
}

function validateMailboxType(_, value, callback) {
  if (!["icloud", "custom"].includes(String(value || "").trim())) {
    callback(new Error("请选择邮箱类型"));
    return;
  }
  callback();
}

function normalizeSuffix(value) {
  return String(value || "").trim().replace(/^@+/, "").toLowerCase();
}

function validateEmailSuffix(_, value, callback) {
  if (!isCustomMailbox.value) {
    callback();
    return;
  }
  const normalized = normalizeSuffix(value);
  if (!normalized) {
    callback(new Error("请填写邮箱后缀"));
    return;
  }
  if (
    normalized.length > 241 ||
    !/^(?=.{1,241}$)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$/.test(
      normalized,
    )
  ) {
    callback(new Error("邮箱后缀格式不正确"));
    return;
  }
  callback();
}

function validateName(_, value, callback) {
  if (runeLength(value) > 80) {
    callback(new Error("备注不能超过 80 个字符"));
    return;
  }
  callback();
}

function validatePassword(_, value, callback) {
  const password = String(value ?? "");
  const passwordMissing = usesGenericIMAPPassword.value
    ? password.length === 0
    : !password.trim();
  if (!isEdit.value && passwordMissing) {
    callback(
      new Error(
        usesGenericIMAPPassword.value
          ? "请填写 IMAP 密码"
          : "请填写 App 专用密码",
      ),
    );
    return;
  }
  callback();
}

function validateIMAPHost(_, value, callback) {
  const message = validateIMAPHostValue(value);
  if (message) {
    callback(new Error(message));
    return;
  }
  callback();
}

function validateIMAPPort(_, value, callback) {
  const message = validateIMAPPortValue(value);
  if (message) {
    callback(new Error(message));
    return;
  }
  callback();
}

function validateAutoLoginField(field) {
  return (_, _value, callback) => {
    const message = autoLoginValidation(form, savedAutoLogin)[field];
    callback(message ? new Error(message) : undefined);
  };
}

const rules = {
  name: [{ validator: validateName, trigger: "blur" }],
  mailboxType: [{ validator: validateMailboxType, trigger: "change" }],
  email: [{ validator: validateEmail, trigger: ["blur", "change"] }],
  emailSuffix: [
    { validator: validateEmailSuffix, trigger: ["blur", "change"] },
  ],
  imapHost: [{ validator: validateIMAPHost, trigger: ["blur", "change"] }],
  imapPort: [{ validator: validateIMAPPort, trigger: ["blur", "change"] }],
  imapUsername: [
    { required: true, message: "请填写 IMAP 用户名", trigger: "blur" },
  ],
  imapPassword: [{ validator: validatePassword, trigger: "blur" }],
  proxyEndpoint: [{ validator: validateProxyEndpoint, trigger: ["blur", "change"] }],
  autoLoginRegion: [{ validator: validateAutoLoginField("autoLoginRegion"), trigger: "change" }],
  autoLoginPassword: [{ validator: validateAutoLoginField("autoLoginPassword"), trigger: "blur" }],
  autoLoginCodeURL: [{ validator: validateAutoLoginField("autoLoginCodeURL"), trigger: "blur" }],
};

function validateProxyEndpoint(_, value, callback) {
  const endpoint = String(value || "").trim();
  if (!form.proxyEnabled) return callback();
  if (!endpoint && (!isEdit.value || !proxy.configured)) return callback(new Error("请填写代理地址"));
  if (form.clearProxyUpstream && form.proxyUpstream.trim()) return callback(new Error("清除前置代理时请留空前置代理地址"));
  callback();
}

async function testProxy() {
  proxyTestResult.value = null;
  if (!form.proxyEnabled) {
    proxyTestResult.value = { ok: true, message: "代理已关闭，当前不会通过代理连接。" };
    return;
  }
  proxyTesting.value = true;
  const started = performance.now();
  try {
    const result = await testAccountProxy(isEdit.value ? route.params.id : undefined, {
      enabled: form.proxyEnabled,
      endpoint: form.proxyEndpoint,
      upstream: form.proxyUpstream,
      clear_upstream: form.clearProxyUpstream,
    }, auth.state.csrfToken);
    const elapsed = Number(result?.duration_ms ?? result?.elapsed_ms);
    const duration = Number.isFinite(elapsed) ? elapsed : Math.round(performance.now() - started);
    const status = Number(result?.status_code);
    proxyTestResult.value = { ok: true, message: `代理连接成功${Number.isInteger(status) ? `，目标 HTTP ${status}` : ""}，耗时 ${duration} ms` };
  } catch (error) {
    proxyTestResult.value = { ok: false, message: `代理连接失败：${error?.message || "请检查代理配置"}` };
  } finally {
    proxyTesting.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    name: "",
    email: "",
    mailboxType: "icloud",
    emailSuffix: "",
    imapHost: DEFAULT_IMAP_HOST,
    imapPort: DEFAULT_IMAP_PORT,
    imapUsername: "",
    imapPassword: "",
    proxyEnabled: false,
    proxyEndpoint: "",
    proxyUpstream: "",
    clearProxyUpstream: false,
    enabled: true,
    autoLoginEnabled: false,
    autoLoginRegion: "global",
    autoLoginPassword: "",
    autoLoginCodeURL: "",
  });
  autoLoginGate.invalidate();
  autoLoginReady.value = !isEdit.value;
  autoLoginLoading.value = false;
  autoLoginLoadError.value = false;
  Object.assign(savedAutoLogin, { passwordConfigured: false, codeURLConfigured: false, channels: [] });
  aliasCount.value = 0;
  storedEnabled.value = true;
  Object.assign(proxy, { configured: false, upstreamConfigured: false });
  proxyTestResult.value = null;
  loadError.value = null;
  submitError.value = null;
  formRef.value?.clearValidate();
}

async function loadAutoLogin() {
  if (!isEdit.value || isCustomMailbox.value) return;
  const accountId = String(route.params.id || "");
  const ticket = autoLoginGate.begin(routeKey());
  autoLoginReady.value = false;
  autoLoginLoading.value = true;
  autoLoginLoadError.value = false;
  try {
    const config = await getAccountAutoLogin(accountId);
    if (!autoLoginGate.isCurrent(ticket, routeKey())) return;
    Object.assign(savedAutoLogin, config);
    form.autoLoginEnabled = config.enabled;
    form.autoLoginRegion = config.region;
    autoLoginReady.value = true;
  } catch {
    if (!autoLoginGate.isCurrent(ticket, routeKey())) return;
    autoLoginLoadError.value = true;
  } finally {
    if (autoLoginGate.isCurrent(ticket, routeKey())) autoLoginLoading.value = false;
  }
}

async function loadAccount() {
  if (!isEdit.value) return;
  const accountId = String(route.params.id || "");
  const ticket = loadGate.begin(routeKey());
  loading.value = true;
  loadError.value = null;
  try {
    const { account } = await getAccount(accountId);
    if (!loadGate.isCurrent(ticket, routeKey())) return;
    const imapEndpoint = normalizeIMAPEndpoint(
      account.imapHost,
      account.imapPort,
    );
    Object.assign(form, {
      name: account.name,
      email: account.mailboxType === "custom" ? "" : account.email,
      mailboxType: account.mailboxType || "icloud",
      emailSuffix: account.emailSuffix || "",
      imapHost: imapEndpoint.host,
      imapPort: imapEndpoint.port,
      imapUsername: account.imapUsername,
      imapPassword: "",
      enabled: account.enabled,
      proxyEnabled: account.proxy.enabled,
      proxyEndpoint: "",
      proxyUpstream: "",
      clearProxyUpstream: false,
    });
    Object.assign(proxy, { configured: account.proxy.configured, upstreamConfigured: account.proxy.upstreamConfigured });
    storedEnabled.value = Boolean(account.enabled);
    aliasCount.value = account.aliasCount;
    if (!isCustomMailbox.value) loadAutoLogin();
  } catch (error) {
    if (!loadGate.isCurrent(ticket, routeKey())) return;
    loadError.value = error;
  } finally {
    if (loadGate.isCurrent(ticket, routeKey())) {
      loading.value = false;
    }
  }
}

async function submit() {
  if (!submitLock.acquire()) return;
  const submittedRouteKey = routeKey();
  const submittedAccountId = String(route.params.id || "");
  submitting.value = true;
  try {
    submitError.value = null;
    if (!isCustomMailbox.value && (!autoLoginReady.value || autoLoginLoading.value)) return;
    const valid = await formRef.value?.validate().catch(() => false);
    if (!valid || submittedRouteKey !== routeKey()) return;

    if (isEdit.value && storedEnabled.value && !form.enabled) {
      await ElMessageBox.confirm(
        "停用会暂停此主号的收件连接，并从隐藏邮箱列表和邮箱池入池候选中隐藏下属邮箱。重新启用后，恢复邮箱列表显示，并按停用前快照恢复原启用及池成员状态；原本单独停用的邮箱仍保持停用。不会删除邮箱、邮件或领取历史。",
        "确认停用主号",
        { type: "warning", confirmButtonText: "停用主号", cancelButtonText: "取消" },
      );
    }
    if (!viewActive || submittedRouteKey !== routeKey()) return;

    const imapEndpoint = normalizeIMAPEndpoint(form.imapHost, form.imapPort);
    const payload = {
      name: form.name.trim(),
      mailbox_type: form.mailboxType,
      imap_host: imapEndpoint.host,
      imap_port: imapEndpoint.port,
      imap_username: form.imapUsername.trim(),
      imap_password: usesGenericIMAPPassword.value
        ? form.imapPassword
        : form.imapPassword.trim(),
      auto_login: autoLoginPayload(form),
      proxy: {
        enabled: Boolean(form.proxyEnabled),
        ...(form.proxyEndpoint.trim() ? { endpoint: form.proxyEndpoint.trim() } : {}),
        ...(form.proxyUpstream.trim() ? { upstream: form.proxyUpstream.trim() } : {}),
        ...(form.clearProxyUpstream ? { clear_upstream: true } : {}),
      },
    };
    if (isCustomMailbox.value) {
      payload.email_suffix = normalizeSuffix(form.emailSuffix);
    } else {
      payload.email = form.email.trim();
    }
    const account = isEdit.value
      ? await updateAccount(
          submittedAccountId,
          { ...payload, enabled: Boolean(form.enabled) },
          auth.state.csrfToken,
        )
      : await createAccount(payload, auth.state.csrfToken);
    if (!viewActive || submittedRouteKey !== routeKey()) return;
    form.imapPassword = "";
    clearAutoLoginSecrets(form);
    successMessage(
      isEdit.value && !form.enabled
        ? "主号已停用；收件已暂停，下属邮箱已从隐藏邮箱列表和入池候选中隐藏，原状态及历史记录保留。"
        : isEdit.value
          ? "主号设置已保存。"
          : "主号已添加。",
    );
    await router.replace({ name: "account-detail", params: { id: account.id } });
  } catch (error) {
    if (!viewActive || submittedRouteKey !== routeKey()) return;
    if (confirmationCancelled(error)) return;
    form.imapPassword = "";
    clearAutoLoginSecrets(form);
    submitError.value = error;
    formRef.value?.clearValidate("imapPassword");
  } finally {
    submitting.value = false;
    submitLock.release();
  }
}

function cancel() {
  clearAutoLoginSecrets(form);
  if (isEdit.value) {
    router.push({ name: "account-detail", params: { id: route.params.id } });
  } else {
    router.push({ name: "accounts" });
  }
}

watch(
  () => [route.name, route.params.id],
  () => {
    loadGate.invalidate();
    loading.value = false;
    resetForm();
    if (isEdit.value) loadAccount();
  },
);

watch(() => form.autoLoginEnabled, (enabled) => {
  if (!enabled) clearAutoLoginSecrets(form);
  formRef.value?.clearValidate(["autoLoginRegion", "autoLoginPassword", "autoLoginCodeURL"]);
});

watch(() => form.mailboxType, (mailboxType) => {
  if (mailboxType !== "custom") {
    if (isEdit.value && !autoLoginReady.value && !autoLoginLoading.value && !loading.value) loadAutoLogin();
    return;
  }
  form.autoLoginEnabled = false;
  clearAutoLoginSecrets(form);
});

onMounted(() => {
  autoLoginReady.value = !isEdit.value;
  if (isEdit.value) loadAccount();
});

onBeforeUnmount(() => {
  viewActive = false;
  loadGate.deactivate();
  autoLoginGate.deactivate();
  form.imapPassword = "";
  clearAutoLoginSecrets(form);
});
</script>

<style scoped>
.account-form-page {
  width: 100%;
  max-width: 960px;
  margin-inline: auto;
}

.account-form-page :deep(.form-grid) {
  align-items: start;
  row-gap: 20px;
}

.account-form-page :deep(.form-grid > .el-form-item) {
  min-width: 0;
  margin-bottom: 0;
}

.account-form-page :deep(.el-input__wrapper),
.account-form-page :deep(.el-select .el-select__wrapper),
.account-form-page :deep(.el-button) {
  min-height: 38px;
}

.account-form-page :deep(.imap-service-fields) {
  grid-template-columns: minmax(0, 1fr) 112px;
}

.account-form-page .form-actions {
  margin-top: 20px;
}

.account-form-page :deep(.imap-service-fields .el-form-item) {
  margin-bottom: 0;
}

.account-form-page :deep(.imap-service-fields .el-form-item__error) {
  position: static;
  flex: 0 0 100%;
  width: 100%;
}

.account-form-page :deep(.mailbox-route-summary) {
  padding: 12px 14px;
  background: var(--surface-subtle);
  border: 1px solid var(--border);
  border-radius: 5px;
}

.auto-login-card {
  display: grid;
  gap: 16px;
  min-width: 0;
  padding: 20px;
  background: var(--surface-subtle);
  border: 1px solid var(--border);
  border-radius: 16px;
}

.proxy-card {
  display: grid;
  gap: 14px;
  min-width: 0;
  padding: 18px;
  border: 1px solid var(--border);
  border-radius: 8px;
}

.proxy-card h2 { margin: 0; font-size: 16px; }
.proxy-fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 20px; }
.proxy-fields :deep(.el-form-item) { min-width: 0; margin-bottom: 0; }
.proxy-test-row { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
.proxy-test-success { color: var(--success); }
.proxy-test-error { color: var(--danger); }

.auto-login-card h2 {
  margin: 0;
  font-size: 16px;
}

.auto-login-fields {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 20px;
}

.auto-login-fields :deep(.el-form-item) {
  min-width: 0;
  margin-bottom: 0;
}

.auto-login-fields :deep(.el-form-item__error) {
  position: static;
}

.auto-login-channels {
  display: grid;
  gap: 10px;
}

.auto-login-channel {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 14px;
  font-size: 12px;
}

.auto-login-load-error p {
  margin-top: 0;
}

@media (max-width: 720px) {
  .account-form-page :deep(.imap-service-fields) {
    grid-template-columns: minmax(0, 1fr) 88px;
  }

  .auto-login-fields {
    grid-template-columns: minmax(0, 1fr);
  }

  .proxy-fields { grid-template-columns: minmax(0, 1fr); }
}
</style>
