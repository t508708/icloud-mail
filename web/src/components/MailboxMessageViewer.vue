<template>
  <div class="inbox-viewer" :class="{ 'inbox-viewer--compact': compact }">
    <header v-if="alias" class="inbox-alias-bar" aria-label="当前邮箱信息">
      <div class="inbox-alias-bar__identity">
        <div class="inbox-alias-bar__address-row">
          <strong class="inbox-alias-bar__address">{{ alias.address }}</strong>
          <el-tooltip content="复制邮箱地址" placement="top">
            <el-button
              size="small"
              text
              :icon="CopyDocument"
              aria-label="复制邮箱地址"
              @click="copyAddress"
            >
              复制
            </el-button>
          </el-tooltip>
          <el-tag v-if="alias.label" size="small" type="info" effect="plain">
            {{ alias.label }}
          </el-tag>
          <el-tag v-if="aliasGroupName" size="small" effect="plain">
            {{ aliasGroupName }}
          </el-tag>
          <el-tag
            size="small"
            :type="mailTransport === 'webmail' ? 'primary' : 'info'"
            effect="light"
          >
            {{ mailTransport === "webmail" ? "网页收件" : "IMAP" }}
          </el-tag>
          <el-tag v-if="!aliasEnabled || !accountEnabled" size="small" type="warning" effect="plain">
            {{ !accountEnabled ? "主号已停用" : "邮箱已停用" }}
          </el-tag>
        </div>

        <div class="inbox-alias-bar__meta">
          <span v-if="accountEmail" class="inbox-alias-bar__meta-item">
            所属主号：
            <button
              type="button"
              class="inbox-inline-link"
              @click="$emit('open-account', alias)"
            >
              {{ accountEmail }}
            </button>
          </span>
          <span class="inbox-alias-bar__meta-item" :title="lastSyncedFull">
            最后同步：{{ lastSyncedLabel }}
          </span>
          <span v-if="totalArchived > 0" class="inbox-alias-bar__meta-item">
            已归档 {{ totalArchived }} 封
          </span>
        </div>
      </div>

      <div class="inbox-alias-bar__actions">
        <el-button
          type="primary"
          :icon="Refresh"
          :loading="syncing"
          :disabled="loading"
          @click="$emit('sync')"
        >
          同步最新邮件
        </el-button>
        <el-button
          v-if="sharePath"
          :icon="Link"
          @click="$emit('copy-share', alias)"
        >
          复制查件分享链接
        </el-button>
        <el-button
          v-if="otpUrlPath"
          :icon="Key"
          @click="$emit('copy-otp-link', alias)"
        >
          复制 OTP 接口链接
        </el-button>
        <el-button
          v-if="compact"
          :icon="FullScreen"
          @click="$emit('open-full-page', alias)"
        >
          独立页打开
        </el-button>
      </div>
    </header>

    <el-alert
      v-if="syncWarning"
      class="inbox-warning-strip"
      type="warning"
      :title="syncWarning"
      show-icon
      :closable="false"
      role="status"
    />

    <div v-if="activeTag" class="inbox-filter-strip" role="status" aria-live="polite">
      <span class="inbox-filter-strip__label">
        当前子地址筛选：
        <span class="inbox-tag-pill">+{{ activeTag }}</span>
      </span>
      <el-button size="small" text type="primary" :icon="Close" @click="$emit('clear-tag')">
        清除筛选并查看全部
      </el-button>
    </div>

    <div class="inbox-workspace" :class="{ 'inbox-workspace--compact': compact }">
      <aside class="inbox-list-pane" aria-label="最近 5 封邮件列表">
        <div class="inbox-pane-header">
          <strong>最近邮件</strong>
          <span class="inbox-pane-header__count">
            {{ messages.length }} 封
          </span>
        </div>

        <div v-if="loading && messages.length === 0" class="inbox-list-skeleton" aria-busy="true">
          <el-skeleton :rows="6" animated />
        </div>

        <div v-else-if="messages.length === 0" class="inbox-list-empty">
          <EmptyState
            class="empty-state--compact"
            level="h3"
            :title="activeTag ? `未找到 +${activeTag} 的邮件` : '暂无归档邮件'"
            :description="activeTag ? '可清除子地址筛选查看该邮箱的全部邮件，或点击同步最新邮件。' : '点击右上角“同步最新邮件”从上游拉取最近 5 封邮件。'"
          >
            <div class="button-row">
              <el-button
                v-if="activeTag"
                size="small"
                @click="$emit('clear-tag')"
              >
                查看全部邮件
              </el-button>
              <el-button
                type="primary"
                size="small"
                :icon="Refresh"
                :loading="syncing"
                @click="$emit('sync')"
              >
                同步最新邮件
              </el-button>
            </div>
          </EmptyState>
        </div>

        <ul v-else class="inbox-message-list" role="listbox" aria-label="最近邮件">
          <li v-for="msg in messages" :key="messageKey(msg)">
            <div
              class="inbox-message-card"
              :class="{ 'is-selected': selectedMessage && messageKey(selectedMessage) === messageKey(msg) }"
              role="option"
              tabindex="0"
              :aria-selected="Boolean(selectedMessage && messageKey(selectedMessage) === messageKey(msg))"
              @click="selectMessage(msg)"
              @keydown.enter.prevent="selectMessage(msg)"
              @keydown.space.prevent="selectMessage(msg)"
            >
              <div class="inbox-message-card__top">
                <div class="inbox-message-card__sender">
                  <strong class="inbox-message-card__sender-name">
                    {{ senderDisplay(msg).primary }}
                  </strong>
                  <small v-if="senderDisplay(msg).secondary" class="inbox-message-card__sender-email">
                    {{ senderDisplay(msg).secondary }}
                  </small>
                </div>
                <time
                  class="inbox-message-card__time"
                  :datetime="msg.received_at || undefined"
                  :title="formatTime(msg.received_at, { seconds: true })"
                >
                  {{ formatRelativeMailTime(msg.received_at) }}
                </time>
              </div>

              <div class="inbox-message-card__subject">
                {{ msg.subject || "(无主题)" }}
              </div>

              <p v-if="msg.snippet" class="inbox-message-card__snippet">
                {{ msg.snippet }}
              </p>

              <div class="inbox-message-card__badges">
                <button
                  v-if="msg.otp"
                  type="button"
                  class="inbox-otp-pill"
                  :aria-label="`复制验证码 ${msg.otp}`"
                  @click.stop="copyOtp(msg.otp)"
                >
                  <el-icon aria-hidden="true"><Key /></el-icon>
                  <span>{{ msg.otp }}</span>
                  <el-icon class="inbox-otp-pill__copy" aria-hidden="true"><CopyDocument /></el-icon>
                </button>

                <span
                  v-for="tag in msg.plus_tags || []"
                  :key="tag"
                  class="inbox-tag-pill"
                >
                  +{{ tag }}
                </span>

                <span
                  v-if="msg.attachments && msg.attachments.length"
                  class="inbox-attachment-pill"
                  :title="`${msg.attachments.length} 个附件`"
                >
                  <el-icon aria-hidden="true"><Paperclip /></el-icon>
                  <span>{{ msg.attachments.length }}</span>
                </span>
              </div>
            </div>
          </li>
        </ul>
      </aside>

      <section
        ref="detailPaneRef"
        class="inbox-detail-pane"
        aria-label="邮件正文详情"
        :aria-busy="loading"
      >
        <div v-if="loading && !selectedMessage" class="inbox-detail-skeleton">
          <el-skeleton :rows="10" animated />
        </div>

        <EmptyState
          v-else-if="!selectedMessage"
          level="h3"
          title="选择一封邮件查看详情"
          description="点击左侧列表中的任意邮件，即可安全预览正文、信头与识别出的验证码。"
        />

        <article v-else class="inbox-detail-article">
          <div v-if="selectedMessage.otp" class="inbox-otp-banner" role="region" aria-label="识别到的验证码">
            <div class="inbox-otp-banner__copy">
              <span class="inbox-otp-banner__label">识别到验证码</span>
              <code class="inbox-otp-banner__code">{{ selectedMessage.otp }}</code>
            </div>
            <el-button
              type="primary"
              size="small"
              :icon="CopyDocument"
              @click="copyOtp(selectedMessage.otp)"
            >
              一键复制
            </el-button>
          </div>

          <header class="inbox-detail-header">
            <div class="inbox-detail-header__title-row">
              <h3 class="inbox-detail-header__subject">
                {{ selectedMessage.subject || "(无主题)" }}
              </h3>
              <el-button
                size="small"
                text
                :aria-expanded="headersExpanded"
                @click="headersExpanded = !headersExpanded"
              >
                {{ headersExpanded ? "收起信头" : "展开信头" }}
                <el-icon class="el-icon--right" aria-hidden="true">
                  <ArrowUp v-if="headersExpanded" />
                  <ArrowDown v-else />
                </el-icon>
              </el-button>
            </div>

            <div class="inbox-detail-header__summary">
              <span>发件人：<strong>{{ formatMailParty(selectedMessage.from) || "未知发件人" }}</strong></span>
              <time
                :datetime="selectedMessage.received_at || undefined"
                :title="formatTime(selectedMessage.received_at, { seconds: true })"
              >
                {{ formatTime(selectedMessage.received_at, { seconds: true }) }}
              </time>
            </div>

            <dl v-if="headersExpanded" class="inbox-headers-grid">
              <div>
                <dt>发件人</dt>
                <dd>{{ formatMailParty(selectedMessage.from) || "-" }}</dd>
              </div>
              <div>
                <dt>原始收件地址</dt>
                <dd>
                  <template v-if="toEntries(selectedMessage.to).length">
                    <span
                      v-for="(entry, idx) in toEntries(selectedMessage.to)"
                      :key="`${entry.raw}-${idx}`"
                      class="inbox-recipient-item"
                    >
                      <template v-if="entry.name">{{ entry.name }} &lt;</template>
                      <template v-if="entry.tag">
                        <span>{{ entry.localBase }}</span><mark class="inbox-recipient-tag">+{{ entry.tag }}</mark><span>@{{ entry.domain }}</span>
                      </template>
                      <template v-else>{{ entry.email || entry.raw }}</template>
                      <template v-if="entry.name">&gt;</template>
                      <template v-if="idx < toEntries(selectedMessage.to).length - 1">，</template>
                    </span>
                  </template>
                  <template v-else>-</template>
                </dd>
              </div>
              <div v-if="selectedMessage.cc && selectedMessage.cc.length">
                <dt>抄送</dt>
                <dd>{{ formatMailParty(selectedMessage.cc) }}</dd>
              </div>
              <div>
                <dt>收件时间</dt>
                <dd>{{ formatTime(selectedMessage.received_at, { seconds: true }) }}</dd>
              </div>
              <div>
                <dt>发件时间</dt>
                <dd>{{ formatTime(selectedMessage.sent_at, { seconds: true }) }}</dd>
              </div>
              <div>
                <dt>邮件大小</dt>
                <dd>
                  {{ formatByteSize(selectedMessage.content_bytes) }}
                  <el-tag v-if="selectedMessage.body_truncated" size="small" type="warning" effect="plain">
                    正文已截断
                  </el-tag>
                </dd>
              </div>
              <div v-if="selectedMessage.attachments && selectedMessage.attachments.length" class="inbox-headers-grid__full">
                <dt>附件列表（{{ selectedMessage.attachments.length }}）</dt>
                <dd class="inbox-attachment-list">
                  <el-tag
                    v-for="(att, idx) in selectedMessage.attachments"
                    :key="`${att.filename || 'att'}-${idx}`"
                    size="small"
                    effect="plain"
                    type="info"
                  >
                    <el-icon aria-hidden="true"><Paperclip /></el-icon>
                    {{ att.filename || "未命名附件" }}
                    <template v-if="att.size !== undefined && att.size !== null">
                      （{{ formatByteSize(att.size) }}）
                    </template>
                  </el-tag>
                </dd>
              </div>
            </dl>
          </header>

          <div class="inbox-view-toolbar">
            <div class="inbox-segmented" role="tablist" aria-label="邮件内容视图">
              <button
                type="button"
                role="tab"
                class="inbox-segmented__btn"
                :class="{ 'is-active': viewMode === 'html' }"
                :aria-selected="viewMode === 'html'"
                :disabled="!selectedMessage.has_html"
                @click="viewMode = 'html'"
              >
                富文本视图
              </button>
              <button
                type="button"
                role="tab"
                class="inbox-segmented__btn"
                :class="{ 'is-active': viewMode === 'text' }"
                :aria-selected="viewMode === 'text'"
                @click="viewMode = 'text'"
              >
                纯文本视图
              </button>
              <button
                type="button"
                role="tab"
                class="inbox-segmented__btn"
                :class="{ 'is-active': viewMode === 'meta' }"
                :aria-selected="viewMode === 'meta'"
                @click="viewMode = 'meta'"
              >
                归档元数据
              </button>
            </div>

            <div class="inbox-view-toolbar__extra">
              <el-button
                v-if="selectedMessage.remote_images_blocked || remoteImages"
                size="small"
                :type="remoteImages ? 'primary' : 'default'"
                plain
                :icon="Picture"
                :loading="loading"
                @click="$emit('toggle-remote-images')"
              >
                {{ remoteImages ? "已允许远程图片（点击屏蔽）" : "加载远程图片" }}
              </el-button>
            </div>
          </div>

          <div
            v-if="showUnavailableNotice"
            class="inbox-content-notice"
            role="status"
          >
            <el-icon aria-hidden="true"><Warning /></el-icon>
            <div>
              <strong>{{ contentStateLabel(selectedMessage.content_state) }}</strong>
              <p v-if="selectedMessage.snippet">
                邮件摘要：{{ selectedMessage.snippet }}
              </p>
              <p v-else>
                该邮件的正文内容当前不可用，但主题、发件人与识别到的验证码仍保留。
              </p>
            </div>
          </div>

          <div v-if="viewMode === 'html'" class="inbox-body-surface">
            <iframe
              v-if="selectedMessage.safe_html"
              sandbox=""
              referrerpolicy="no-referrer"
              class="mail-sandbox-frame"
              :srcdoc="sandboxedSrcdoc"
              title="邮件正文安全预览"
            />
            <div v-else-if="!showUnavailableNotice" class="inbox-body-empty">
              该邮件没有可预览的富文本正文，可切换至“纯文本视图”。
            </div>
          </div>

          <div v-else-if="viewMode === 'text'" class="inbox-body-surface">
            <pre v-if="selectedMessage.text_body || selectedMessage.snippet" class="mail-plain-body">{{ selectedMessage.text_body || selectedMessage.snippet }}</pre>
            <div v-else-if="!showUnavailableNotice" class="inbox-body-empty">
              该邮件没有纯文本正文。
            </div>
          </div>

          <div v-else class="inbox-meta-panel">
            <dl class="detail-grid">
              <div>
                <dt>归档 ID</dt>
                <dd>{{ selectedMessage.id ?? "-" }}</dd>
              </div>
              <div>
                <dt>邮箱 UID</dt>
                <dd>{{ selectedMessage.mailbox_uid ?? "-" }}</dd>
              </div>
              <div>
                <dt>Message-ID</dt>
                <dd><code>{{ selectedMessage.message_id || "-" }}</code></dd>
              </div>
              <div>
                <dt>正文归档状态</dt>
                <dd>{{ contentStateLabel(selectedMessage.content_state) }}</dd>
              </div>
              <div>
                <dt>正文大小</dt>
                <dd>{{ formatByteSize(selectedMessage.content_bytes) }}</dd>
              </div>
              <div>
                <dt>内容标记</dt>
                <dd>
                  {{ selectedMessage.has_html ? "包含 HTML" : "仅纯文本" }}
                  <template v-if="selectedMessage.body_truncated"> · 已截断</template>
                  <template v-if="selectedMessage.remote_images_blocked"> · 远程图片已拦截</template>
                </dd>
              </div>
              <div v-if="selectedMessage.plus_tags && selectedMessage.plus_tags.length">
                <dt>匹配子地址标签</dt>
                <dd>{{ selectedMessage.plus_tags.map((t) => `+${t}`).join(", ") }}</dd>
              </div>
            </dl>
          </div>
        </article>
      </section>
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  ArrowDown,
  ArrowUp,
  Close,
  CopyDocument,
  FullScreen,
  Key,
  Link,
  Paperclip,
  Picture,
  Refresh,
  Warning,
} from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";

import EmptyState from "./EmptyState.vue";
import { copyText } from "../utils/clipboard.js";
import { formatTime } from "../utils/format.js";
import {
  buildSandboxedEmailSrcdoc,
  contentStateLabel,
  formatByteSize,
  formatMailParty,
  formatRelativeMailTime,
  parsePlusTagAddress,
} from "../utils/inbox.js";

const props = defineProps({
  alias: { type: Object, default: null },
  messages: { type: Array, default: () => [] },
  totalArchived: { type: Number, default: 0 },
  syncWarning: { type: String, default: "" },
  loading: { type: Boolean, default: false },
  syncing: { type: Boolean, default: false },
  activeTag: { type: String, default: "" },
  remoteImages: { type: Boolean, default: false },
  compact: { type: Boolean, default: false },
});

defineEmits([
  "sync",
  "clear-tag",
  "toggle-remote-images",
  "copy-share",
  "copy-otp-link",
  "open-full-page",
  "open-account",
]);

const selectedMessageKey = ref("");
const viewMode = ref("html");
const headersExpanded = ref(false);
const detailPaneRef = ref(null);
const isDark = ref(
  typeof document !== "undefined" && document.documentElement.classList.contains("dark"),
);

let themeObserver = null;

onMounted(() => {
  if (typeof document === "undefined" || typeof MutationObserver === "undefined") {
    return;
  }
  const syncTheme = () => {
    isDark.value = document.documentElement.classList.contains("dark");
  };
  syncTheme();
  themeObserver = new MutationObserver(syncTheme);
  themeObserver.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["class"],
  });
});

onBeforeUnmount(() => {
  themeObserver?.disconnect();
  themeObserver = null;
});

function messageKey(msg) {
  if (!msg) return "";
  return String(msg.id ?? msg.mailbox_uid ?? msg.message_id ?? "");
}

const selectedMessage = computed(() => {
  if (!props.messages.length) return null;
  const matched = props.messages.find((item) => messageKey(item) === selectedMessageKey.value);
  return matched || props.messages[0];
});

watch(
  () => props.messages,
  (list) => {
    if (!list.length) {
      selectedMessageKey.value = "";
      return;
    }
    const stillExists = list.some((item) => messageKey(item) === selectedMessageKey.value);
    if (!stillExists) {
      selectedMessageKey.value = messageKey(list[0]);
    }
  },
  { immediate: true },
);

watch(
  selectedMessage,
  (msg, prev) => {
    if (!msg) return;
    if (messageKey(msg) !== messageKey(prev)) {
      viewMode.value = msg.has_html ? "html" : "text";
    } else if (viewMode.value === "html" && !msg.has_html) {
      viewMode.value = "text";
    }
  },
  { immediate: true },
);

function selectMessage(msg) {
  selectedMessageKey.value = messageKey(msg);
  if (typeof window !== "undefined" && window.innerWidth < 900) {
    detailPaneRef.value?.scrollIntoView?.({ behavior: "smooth", block: "nearest" });
  }
}

const aliasGroupName = computed(
  () => props.alias?.group_name || props.alias?.groupName || "",
);
const accountEmail = computed(
  () => props.alias?.account_email || props.alias?.accountEmail || "",
);
const aliasEnabled = computed(() => props.alias?.enabled !== false);
const accountEnabled = computed(
  () => (props.alias?.account_enabled ?? props.alias?.accountEnabled) !== false,
);
const mailTransport = computed(() =>
  (props.alias?.mail_transport || props.alias?.mailTransport) === "webmail"
    ? "webmail"
    : "imap",
);
const sharePath = computed(
  () => props.alias?.inbox_share_path || props.alias?.inboxSharePath || "",
);
const otpUrlPath = computed(
  () => props.alias?.otp_url_path || props.alias?.otpUrlPath || "",
);
const lastSyncedRaw = computed(
  () => props.alias?.last_synced_at || props.alias?.lastSyncedAt || null,
);
const lastSyncedLabel = computed(() =>
  lastSyncedRaw.value ? formatRelativeMailTime(lastSyncedRaw.value) : "尚未同步",
);
const lastSyncedFull = computed(() =>
  lastSyncedRaw.value ? formatTime(lastSyncedRaw.value, { seconds: true }) : "尚未同步",
);

const sandboxedSrcdoc = computed(() =>
  buildSandboxedEmailSrcdoc(selectedMessage.value?.safe_html || "", {
    dark: isDark.value,
  }),
);

const showUnavailableNotice = computed(() => {
  const msg = selectedMessage.value;
  if (!msg) return false;
  const state = String(msg.content_state || "").toLowerCase();
  if (!state || state === "available") return false;
  if (viewMode.value === "html") return !msg.safe_html;
  if (viewMode.value === "text") return !msg.text_body;
  return false;
});

function senderDisplay(msg) {
  const first = Array.isArray(msg?.from) ? msg.from[0] : null;
  if (!first) {
    return { primary: "未知发件人", secondary: "" };
  }
  const name = String(first.name || "").trim();
  const email = String(first.email || "").trim();
  if (name && email && name.toLowerCase() !== email.toLowerCase()) {
    return { primary: name, secondary: email };
  }
  return { primary: email || name || "未知发件人", secondary: "" };
}

function toEntries(list) {
  if (!Array.isArray(list)) return [];
  return list
    .map((item) => {
      const email = String(item?.email || "").trim();
      const name = String(item?.name || "").trim();
      const parsed = parsePlusTagAddress(email);
      const atIdx = email.lastIndexOf("@");
      const plusIdx = atIdx > 0 ? email.slice(0, atIdx).indexOf("+") : -1;
      return {
        raw: email || name,
        name: name && name.toLowerCase() !== email.toLowerCase() ? name : "",
        email,
        tag: parsed.tag,
        localBase: plusIdx > 0 ? email.slice(0, plusIdx) : "",
        domain: atIdx > 0 ? email.slice(atIdx + 1) : "",
      };
    })
    .filter((entry) => entry.raw);
}

async function copyAddress() {
  const address = props.alias?.address;
  if (!address) return;
  if (await copyText(address)) {
    ElMessage.success("已复制邮箱地址");
  } else {
    ElMessage.error("复制失败，请手动复制");
  }
}

async function copyOtp(otp) {
  if (!otp) return;
  if (await copyText(String(otp))) {
    ElMessage.success(`已复制验证码 ${otp}`);
  } else {
    ElMessage.error("复制失败，请手动复制");
  }
}
</script>

<style scoped>
.inbox-viewer {
  display: flex;
  flex-direction: column;
  gap: 14px;
  min-width: 0;
}

.inbox-alias-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 14px 20px;
  padding: 16px 18px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 16px;
  box-shadow: var(--panel-shadow);
}

.inbox-viewer--compact .inbox-alias-bar {
  padding: 12px 14px;
  border-radius: 14px;
}

.inbox-alias-bar__identity {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
  flex: 1 1 300px;
}

.inbox-alias-bar__address-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.inbox-alias-bar__address {
  font-size: 16px;
  font-weight: 700;
  color: var(--text);
  overflow-wrap: anywhere;
}

.inbox-alias-bar__meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px 16px;
  color: var(--text-secondary);
  font-size: 12px;
}

.inbox-inline-link {
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--primary);
  font-weight: 600;
  cursor: pointer;
}

.inbox-inline-link:hover {
  text-decoration: underline;
}

.inbox-alias-bar__actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.inbox-warning-strip {
  border-radius: 12px;
}

.inbox-filter-strip {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 10px;
  padding: 9px 14px;
  background: var(--purple-soft);
  border: 1px solid color-mix(in srgb, var(--purple) 26%, var(--border));
  border-radius: 12px;
  font-size: 13px;
  color: var(--text);
}

.inbox-filter-strip__label {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.inbox-workspace {
  display: grid;
  grid-template-columns: 320px minmax(0, 1fr);
  gap: 16px;
  align-items: stretch;
  min-height: 540px;
}

.inbox-workspace--compact {
  min-height: 460px;
}

.inbox-list-pane,
.inbox-detail-pane {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 16px;
  box-shadow: var(--panel-shadow);
  min-width: 0;
}

.inbox-list-pane {
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.inbox-pane-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 14px;
  border-bottom: 1px solid var(--border);
  background: var(--table-header);
  font-size: 13px;
}

.inbox-pane-header__count {
  color: var(--text-muted);
  font-size: 12px;
}

.inbox-list-skeleton,
.inbox-list-empty,
.inbox-detail-skeleton {
  padding: 16px;
}

.inbox-message-list {
  list-style: none;
  margin: 0;
  padding: 8px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  overflow-y: auto;
  flex: 1 1 auto;
}

.inbox-message-card {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 12px;
  border-radius: 12px;
  border: 1px solid transparent;
  background: transparent;
  cursor: pointer;
  transition: background-color 140ms ease, border-color 140ms ease, box-shadow 140ms ease;
}

.inbox-message-card:hover {
  background: var(--row-hover);
}

.inbox-message-card.is-selected {
  background: var(--primary-soft);
  border-color: color-mix(in srgb, var(--primary) 38%, var(--border));
  box-shadow: var(--control-shadow);
}

.inbox-message-card__top {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 8px;
}

.inbox-message-card__sender {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.inbox-message-card__sender-name {
  font-size: 13px;
  font-weight: 650;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.inbox-message-card__sender-email {
  font-size: 11px;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.inbox-message-card__time {
  flex: 0 0 auto;
  color: var(--text-muted);
  font-size: 11px;
  white-space: nowrap;
}

.inbox-message-card__subject {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  line-height: 1.4;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.inbox-message-card__snippet {
  font-size: 12px;
  color: var(--text-secondary);
  line-height: 1.45;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.inbox-message-card__badges {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 2px;
}

.inbox-otp-pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 9px;
  border-radius: 999px;
  border: 1px solid color-mix(in srgb, var(--success) 35%, var(--border));
  background: var(--success-soft);
  color: var(--success);
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  font-weight: 700;
  cursor: pointer;
}

.inbox-otp-pill:hover {
  filter: brightness(0.96);
}

.inbox-otp-pill__copy {
  opacity: 0.85;
}

.inbox-tag-pill {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--purple-soft);
  color: var(--purple);
  font-size: 11px;
  font-weight: 600;
}

.inbox-attachment-pill {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 2px 7px;
  border-radius: 999px;
  background: var(--surface-subtle);
  color: var(--text-secondary);
  font-size: 11px;
}

.inbox-detail-pane {
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.inbox-detail-article {
  display: flex;
  flex-direction: column;
  flex: 1 1 auto;
  min-height: 0;
}

.inbox-otp-banner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
  margin: 14px 18px 0;
  padding: 12px 16px;
  background: var(--success-soft);
  border: 1px solid color-mix(in srgb, var(--success) 30%, var(--border));
  border-radius: 12px;
}

.inbox-otp-banner__copy {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
}

.inbox-otp-banner__label {
  color: var(--success);
  font-size: 13px;
  font-weight: 600;
}

.inbox-otp-banner__code {
  padding: 3px 10px;
  border-radius: 8px;
  background: var(--surface);
  color: var(--text);
  border: 1px solid color-mix(in srgb, var(--success) 25%, var(--border));
  font-size: 18px;
  font-weight: 750;
  letter-spacing: 0.08em;
}

.inbox-detail-header {
  padding: 16px 18px 14px;
  border-bottom: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.inbox-detail-header__title-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.inbox-detail-header__subject {
  font-size: 18px;
  font-weight: 700;
  line-height: 1.4;
  color: var(--text);
  overflow-wrap: anywhere;
}

.inbox-detail-header__summary {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 8px 16px;
  color: var(--text-secondary);
  font-size: 13px;
}

.inbox-headers-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px 18px;
  padding: 12px 14px;
  background: var(--surface-subtle);
  border: 1px solid var(--border);
  border-radius: 10px;
  font-size: 12px;
}

.inbox-headers-grid__full {
  grid-column: 1 / -1;
}

.inbox-headers-grid dt {
  color: var(--text-muted);
  margin-bottom: 3px;
}

.inbox-headers-grid dd {
  color: var(--text);
  overflow-wrap: anywhere;
}

.inbox-recipient-tag {
  padding: 0 4px;
  border-radius: 4px;
  background: var(--purple-soft);
  color: var(--purple);
  font-weight: 650;
}

.inbox-attachment-list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.inbox-view-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 10px;
  padding: 10px 18px;
  border-bottom: 1px solid var(--border);
  background: var(--surface-subtle);
}

.inbox-segmented {
  display: inline-flex;
  padding: 3px;
  border-radius: 10px;
  background: var(--surface);
  border: 1px solid var(--border);
  gap: 2px;
}

.inbox-segmented__btn {
  padding: 5px 12px;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--text-secondary);
  font-size: 12px;
  font-weight: 560;
  cursor: pointer;
  transition: background-color 120ms ease, color 120ms ease;
}

.inbox-segmented__btn:hover:not(:disabled) {
  color: var(--text);
  background: var(--surface-subtle);
}

.inbox-segmented__btn.is-active {
  color: var(--primary);
  background: var(--primary-soft);
  font-weight: 650;
}

.inbox-segmented__btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.inbox-content-notice {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin: 14px 18px 0;
  padding: 12px 14px;
  border-radius: 10px;
  background: var(--warning-soft);
  color: var(--warning);
  font-size: 13px;
  line-height: 1.5;
}

.inbox-content-notice .el-icon {
  margin-top: 2px;
  flex: 0 0 auto;
}

.inbox-content-notice p {
  margin-top: 4px;
  color: var(--text-secondary);
  font-size: 12px;
}

.inbox-body-surface {
  flex: 1 1 auto;
  display: flex;
  flex-direction: column;
  min-height: 360px;
}

.mail-sandbox-frame {
  width: 100%;
  flex: 1 1 auto;
  min-height: 420px;
  border: 0;
  background: var(--surface);
}

.inbox-workspace--compact .mail-sandbox-frame {
  min-height: 340px;
}

.mail-plain-body {
  margin: 0;
  padding: 18px 20px;
  white-space: pre-wrap;
  word-break: break-word;
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
  font-size: 13px;
  line-height: 1.65;
  color: var(--text);
  overflow: auto;
}

.inbox-body-empty {
  padding: 36px 20px;
  text-align: center;
  color: var(--text-muted);
  font-size: 13px;
}

.inbox-meta-panel {
  padding: 18px;
}

@media (max-width: 900px) {
  .inbox-workspace {
    grid-template-columns: minmax(0, 1fr);
    min-height: 0;
  }

  .inbox-message-list {
    max-height: 320px;
  }

  .inbox-headers-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
