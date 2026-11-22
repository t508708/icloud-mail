<template>
  <section class="page-stack dashboard-page" aria-labelledby="dashboard-section-title">
    <SectionHeader
      id="dashboard-section-title"
      title="运行概览"
      description="实时掌握主号收件健康、Apple 双通道连接、邮箱池水位与邮件归档存储配额。"
    >
      <template #actions>
        <el-button :icon="Message" @click="goTo({ name: 'inbox' })">
          收件查看
        </el-button>
        <el-button type="primary" :icon="Plus" @click="goTo({ name: 'account-new' })">
          添加主号
        </el-button>
        <el-tooltip content="刷新运行概览" placement="bottom">
          <el-button
            :icon="Refresh"
            circle
            :loading="loading"
            aria-label="刷新运行概览"
            @click="loadSummary()"
          />
        </el-tooltip>
      </template>
    </SectionHeader>

    <div v-if="loading && !summary" class="data-panel loading-panel">
      <el-skeleton :rows="6" animated role="status" aria-label="正在加载运行概览" />
    </div>

    <div v-else-if="loadError && !summary" class="load-failed">
      <RequestAlert :error="loadError" />
      <el-button :icon="Refresh" @click="loadSummary()">重新加载</el-button>
    </div>

    <template v-else-if="summary">
      <RequestAlert
        v-if="loadError"
        :error="loadError"
        closable
        @close="loadError = null"
      />

      <div class="dashboard-kpi-grid" aria-label="核心运行指标">
        <router-link :to="{ name: 'accounts' }" class="overview-card overview-card--link">
          <div class="overview-card__head">
            <span class="overview-card__icon overview-card__icon--primary" aria-hidden="true">
              <el-icon><Connection /></el-icon>
            </span>
            <h2>主号与连接健康</h2>
            <span class="overview-card__cta">
              查看 <el-icon aria-hidden="true"><ArrowRight /></el-icon>
            </span>
          </div>

          <div class="overview-card__metric">
            <strong>{{ accounts.enabled }}</strong>
            <span class="overview-card__denominator">/ {{ accounts.total }}</span>
            <span class="overview-card__unit">启用主号</span>
          </div>

          <div class="overview-badge-list">
            <span class="overview-pill overview-pill--success">
              IMAP 正常 {{ accounts.imap_healthy }}
            </span>
            <span
              class="overview-pill"
              :class="accounts.imap_auth_paused > 0 ? 'overview-pill--danger' : 'overview-pill--muted'"
            >
              认证异常 {{ accounts.imap_auth_paused }}
            </span>
            <span class="overview-pill">
              Web 目录已连 {{ accounts.apple_web_connected }}
            </span>
            <span class="overview-pill">
              Account 新通道已连 {{ accounts.apple_account_connected }}
            </span>
            <span class="overview-pill">
              独立代理启用 {{ accounts.proxy_enabled }}
            </span>
          </div>
        </router-link>

        <router-link :to="{ name: 'aliases' }" class="overview-card overview-card--link">
          <div class="overview-card__head">
            <span class="overview-card__icon overview-card__icon--primary" aria-hidden="true">
              <el-icon><Message /></el-icon>
            </span>
            <h2>隐藏邮箱与产出实绩</h2>
            <span class="overview-card__cta">
              查看 <el-icon aria-hidden="true"><ArrowRight /></el-icon>
            </span>
          </div>

          <div class="overview-card__metric">
            <strong>{{ aliases.enabled }}</strong>
            <span class="overview-card__denominator">/ {{ aliases.total }}</span>
            <span class="overview-card__unit">启用邮箱</span>
          </div>

          <dl class="overview-mini-stats">
            <div class="overview-mini-stats__highlight">
              <dt>近 1 小时新建</dt>
              <dd>{{ aliases.created_last_hour }}</dd>
            </div>
            <div class="overview-mini-stats__highlight">
              <dt>今日新建</dt>
              <dd>{{ aliases.created_today }}</dd>
            </div>
            <div>
              <dt>已收件活跃邮箱</dt>
              <dd>{{ aliases.with_mail }}</dd>
            </div>
          </dl>
        </router-link>

        <router-link :to="{ name: 'pool' }" class="overview-card overview-card--link">
          <div class="overview-card__head">
            <span class="overview-card__icon overview-card__icon--purple" aria-hidden="true">
              <el-icon><Tickets /></el-icon>
            </span>
            <h2>项目邮箱池水位</h2>
            <span class="overview-card__cta">
              查看 <el-icon aria-hidden="true"><ArrowRight /></el-icon>
            </span>
          </div>

          <div class="overview-card__metric">
            <strong>{{ pool.available }}</strong>
            <span class="overview-card__unit">空闲可领</span>
          </div>

          <dl class="overview-mini-stats overview-mini-stats--two">
            <div>
              <dt>租用待确认</dt>
              <dd>{{ pool.leased }}</dd>
            </div>
            <div>
              <dt>独占已用</dt>
              <dd>{{ pool.used }}</dd>
            </div>
            <div>
              <dt>多项目共享关联</dt>
              <dd>{{ pool.shared_links }}</dd>
            </div>
            <div>
              <dt>启用项目数</dt>
              <dd>{{ pool.projects_enabled }} / {{ pool.projects_total }}</dd>
            </div>
          </dl>
        </router-link>

        <article class="overview-card">
          <div class="overview-card__head">
            <span class="overview-card__icon overview-card__icon--success" aria-hidden="true">
              <el-icon><FolderOpened /></el-icon>
            </span>
            <h2>邮件归档存储配额</h2>
            <el-tag size="small" :type="archiveTagType" effect="light">
              {{ archivePercentText }}
            </el-tag>
          </div>

          <div class="overview-card__metric overview-card__metric--compact">
            <strong>{{ formatBytes(archive.content_bytes) }}</strong>
            <span class="overview-card__denominator">
              / {{ archive.content_limit > 0 ? formatBytes(archive.content_limit) : "未限制" }}
            </span>
          </div>

          <el-progress
            :percentage="archivePercent"
            :stroke-width="8"
            :show-text="false"
            :color="archiveProgressColor"
            :aria-label="`归档存储占用 ${archivePercentText}`"
          />

          <dl class="overview-mini-stats overview-mini-stats--two">
            <div>
              <dt>归档邮件总数</dt>
              <dd>{{ archive.message_count }}</dd>
            </div>
            <div>
              <dt>已淘汰旧正文数</dt>
              <dd>{{ archive.evicted_count }}</dd>
            </div>
          </dl>

          <div class="overview-card__footer">
            <el-button
              size="small"
              :loading="gcLoading"
              :disabled="gcLoading"
              @click="runArchiveGc"
            >
              整理归档与缓存
            </el-button>
          </div>
        </article>
      </div>

      <section class="section-block radar-panel" aria-labelledby="radar-panel-title">
        <template v-if="attentionAccounts.length > 0">
          <div class="radar-panel__header">
            <div class="radar-panel__title-group">
              <span class="radar-panel__alert-icon" aria-hidden="true">
                <el-icon><WarningFilled /></el-icon>
              </span>
              <div>
                <div class="radar-panel__title-row">
                  <h3 id="radar-panel-title">待处理异常主号</h3>
                  <el-tag type="danger" effect="light" size="small">
                    {{ attentionAccounts.length }} 项待处理
                  </el-tag>
                </div>
                <p>检测到以下主号需要人工介入或凭据更新，请优先处理以免影响自动收件。</p>
              </div>
            </div>
            <span class="radar-panel__timestamp">更新于 {{ formattedGeneratedAt }}</span>
          </div>

          <ul class="attention-list">
            <li v-for="item in attentionAccounts" :key="item.id" class="attention-item">
              <div class="attention-item__main">
                <div class="attention-item__identity">
                  <strong>{{ item.name || "未填写备注" }}</strong>
                  <code class="key-prefix">{{ item.email }}</code>
                  <el-tag :type="reasonMeta(item.reason).type" size="small" effect="light">
                    {{ reasonMeta(item.reason).label }}
                  </el-tag>
                </div>
                <p class="attention-item__detail">
                  {{ item.detail || "请检查主号连接或认证状态" }}
                </p>
              </div>
              <div class="attention-item__action">
                <el-button
                  type="primary"
                  plain
                  size="small"
                  @click="goTo({ name: 'account-detail', params: { id: item.id } })"
                >
                  前往修复
                </el-button>
              </div>
            </li>
          </ul>
        </template>

        <template v-else>
          <div class="health-banner" role="status">
            <div class="health-banner__content">
              <span class="health-banner__icon" aria-hidden="true">
                <el-icon><CircleCheckFilled /></el-icon>
              </span>
              <div>
                <h3 id="radar-panel-title">所有启用主号收件与连接状态正常</h3>
                <p>
                  未发现 IMAP 认证暂停、Apple 会话失效或同步阻断，最近更新于 {{ formattedGeneratedAt }}。
                </p>
              </div>
            </div>
          </div>

          <div class="workflow-grid" aria-label="常用工作流快捷入口">
            <router-link
              v-for="entry in workflowEntries"
              :key="entry.title"
              :to="entry.to"
              class="workflow-card"
            >
              <span class="workflow-card__icon" :class="entry.iconClass" aria-hidden="true">
                <el-icon><component :is="entry.icon" /></el-icon>
              </span>
              <div class="workflow-card__copy">
                <strong>{{ entry.title }}</strong>
                <small>{{ entry.description }}</small>
              </div>
              <el-icon class="workflow-card__arrow" aria-hidden="true"><ArrowRight /></el-icon>
            </router-link>
          </div>
        </template>
      </section>
    </template>
  </section>
</template>

<script setup>
import {
  ArrowRight,
  CircleCheckFilled,
  Connection,
  FolderOpened,
  Lock,
  Message,
  Plus,
  Refresh,
  Setting,
  Tickets,
  WarningFilled,
} from "@element-plus/icons-vue";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

import { apiRequest } from "../api/client.js";
import RequestAlert from "../components/RequestAlert.vue";
import SectionHeader from "../components/SectionHeader.vue";
import { useAuth } from "../stores/auth.js";
import { createActionLock, createLatestRequestGate } from "../utils/asyncState.js";
import { showRequestError, successMessage } from "../utils/feedback.js";
import { formatTime } from "../utils/format.js";
import { createLiveRefresh } from "../utils/liveRefresh.js";

const router = useRouter();
const auth = useAuth();

const summary = ref(null);
const loading = ref(false);
const gcLoading = ref(false);
const loadError = ref(null);

const loadGate = createLatestRequestGate();
const actionLock = createActionLock();
let summaryAbortController = null;
let viewActive = true;

function toNonNegInt(value) {
  const num = Number(value);
  return Number.isFinite(num) && num >= 0 ? Math.trunc(num) : 0;
}

function formatBytes(value) {
  const bytes = Number(value);
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return "0 B";
  }
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = bytes;
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024;
    unitIndex += 1;
  }
  if (unitIndex === 0) {
    return `${Math.trunc(size)} B`;
  }
  const decimals = unitIndex >= 3 ? 2 : 1;
  return `${size.toFixed(decimals).replace(/\.0+$/, "")} ${units[unitIndex]}`;
}

const accounts = computed(() => {
  const raw = summary.value?.accounts || {};
  return {
    total: toNonNegInt(raw.total),
    enabled: toNonNegInt(raw.enabled),
    disabled: toNonNegInt(raw.disabled),
    imap_healthy: toNonNegInt(raw.imap_healthy),
    imap_auth_paused: toNonNegInt(raw.imap_auth_paused),
    apple_web_connected: toNonNegInt(raw.apple_web_connected),
    apple_account_connected: toNonNegInt(raw.apple_account_connected),
    auto_create_enabled: toNonNegInt(raw.auto_create_enabled),
    proxy_enabled: toNonNegInt(raw.proxy_enabled),
  };
});

const aliases = computed(() => {
  const raw = summary.value?.aliases || {};
  return {
    total: toNonNegInt(raw.total),
    enabled: toNonNegInt(raw.enabled),
    with_mail: toNonNegInt(raw.with_mail),
    created_last_hour: toNonNegInt(raw.created_last_hour),
    created_today: toNonNegInt(raw.created_today),
  };
});

const pool = computed(() => {
  const raw = summary.value?.pool || {};
  return {
    available: toNonNegInt(raw.available),
    leased: toNonNegInt(raw.leased),
    used: toNonNegInt(raw.used),
    paused: toNonNegInt(raw.paused),
    shared_links: toNonNegInt(raw.shared_links),
    projects_total: toNonNegInt(raw.projects_total),
    projects_enabled: toNonNegInt(raw.projects_enabled),
  };
});

const archive = computed(() => {
  const raw = summary.value?.archive || {};
  return {
    content_bytes: toNonNegInt(raw.content_bytes),
    content_limit: toNonNegInt(raw.content_limit),
    message_count: toNonNegInt(raw.message_count),
    evicted_count: toNonNegInt(raw.evicted_count),
  };
});

const archivePercent = computed(() => {
  const limit = archive.value.content_limit;
  if (limit <= 0) {
    return 0;
  }
  const ratio = (archive.value.content_bytes / limit) * 100;
  return Math.min(100, Math.max(0, Math.round(ratio * 10) / 10));
});

const archivePercentText = computed(() => {
  if (archive.value.content_limit <= 0) {
    return "无上限";
  }
  return `${archivePercent.value}%`;
});

const archiveProgressColor = computed(() => {
  if (archivePercent.value >= 90) {
    return "var(--danger)";
  }
  if (archivePercent.value >= 75) {
    return "var(--warning)";
  }
  return "var(--primary)";
});

const archiveTagType = computed(() => {
  if (archivePercent.value >= 90) {
    return "danger";
  }
  if (archivePercent.value >= 75) {
    return "warning";
  }
  return "primary";
});

const attentionAccounts = computed(() => {
  const items = summary.value?.attention_accounts;
  return Array.isArray(items) ? items : [];
});

const formattedGeneratedAt = computed(() => {
  return formatTime(summary.value?.generated_at, { seconds: true });
});

const workflowEntries = [
  {
    title: "收件查看最近 5 封邮件",
    description: "按收件箱或验证码快速检索最新转发邮件",
    to: { name: "inbox" },
    icon: Message,
    iconClass: "workflow-card__icon--primary",
  },
  {
    title: "配置分项目接码规则",
    description: "管理项目发件人与主题过滤及邮箱池接入",
    to: { name: "pool" },
    icon: Setting,
    iconClass: "workflow-card__icon--purple",
  },
  {
    title: "查看全部运行日志",
    description: "排查主号同步、代理与接口调用链路",
    to: { name: "logs" },
    icon: Tickets,
    iconClass: "workflow-card__icon--primary",
  },
  {
    title: "安全设置",
    description: "管理当前管理员账号与登录凭据",
    to: { name: "security" },
    icon: Lock,
    iconClass: "workflow-card__icon--success",
  },
];

function reasonMeta(reason) {
  switch (String(reason || "")) {
    case "imap_auth_paused":
      return { label: "IMAP 认证暂停", type: "danger" };
    case "apple_auth_required":
      return { label: "Apple 会话需重新认证", type: "warning" };
    case "auto_login_action":
      return { label: "自动登录需处理", type: "warning" };
    case "sync_error":
      return { label: "同步异常", type: "danger" };
    default:
      return { label: reason || "状态异常", type: "warning" };
  }
}

function goTo(location) {
  void router.push(location);
}

async function loadSummary({ silent = false } = {}) {
  const ticket = loadGate.begin("dashboard-summary");
  summaryAbortController?.abort();
  const abortController = new AbortController();
  summaryAbortController = abortController;

  if (!silent) {
    loading.value = true;
    loadError.value = null;
  }

  try {
    const data = await apiRequest("/dashboard/summary", {
      signal: abortController.signal,
    });
    if (!viewActive || !loadGate.isCurrent(ticket, "dashboard-summary")) {
      return false;
    }
    summary.value = data || {};
    loadError.value = null;
    return true;
  } catch (error) {
    if (error?.name === "AbortError") {
      return false;
    }
    if (viewActive && loadGate.isCurrent(ticket, "dashboard-summary") && !silent) {
      loadError.value = error;
    }
    return false;
  } finally {
    if (
      summaryAbortController === abortController &&
      loadGate.isCurrent(ticket, "dashboard-summary")
    ) {
      loading.value = false;
    }
    if (summaryAbortController === abortController) {
      summaryAbortController = null;
    }
  }
}

async function runArchiveGc() {
  if (!actionLock.acquire("archive-gc")) {
    return;
  }
  gcLoading.value = true;
  try {
    const result = await apiRequest("/archive/gc", {
      method: "POST",
      csrfToken: auth.state.csrfToken,
      body: {},
    });
    if (!viewActive) {
      return;
    }
    if (result?.archive && summary.value) {
      summary.value = {
        ...summary.value,
        archive: result.archive,
      };
    }
    const tempFiles = toNonNegInt(result?.cleaned_temp_files);
    const sessions = toNonNegInt(result?.cleaned_sessions);
    const currentBytes = formatBytes(result?.archive?.content_bytes ?? archive.value.content_bytes);
    successMessage(
      `整理完成：清理临时文件 ${tempFiles} 个、过期会话 ${sessions} 个，当前归档占用 ${currentBytes}。`,
      3600,
    );
    void loadSummary({ silent: true });
  } catch (error) {
    if (viewActive) {
      showRequestError(error, "整理归档与缓存失败，请稍后重试。");
    }
  } finally {
    actionLock.release("archive-gc");
    if (viewActive) {
      gcLoading.value = false;
    }
  }
}

const liveRefresh = createLiveRefresh(() => loadSummary({ silent: true }), {
  intervalMs: 30_000,
});

onMounted(() => {
  void loadSummary();
  liveRefresh.start({ immediate: false });
});

onBeforeUnmount(() => {
  viewActive = false;
  loadGate.deactivate();
  summaryAbortController?.abort();
  liveRefresh.stop();
});
</script>

<style scoped>
.dashboard-kpi-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 16px;
}

.overview-card {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 14px;
  padding: 18px;
  color: var(--text);
  background: var(--glass-bg);
  border: 1px solid var(--glass-border);
  border-radius: 20px;
  box-shadow: var(--glass-shadow);
  -webkit-backdrop-filter: blur(20px) saturate(150%);
  backdrop-filter: blur(20px) saturate(150%);
  transition:
    transform 150ms ease,
    border-color 150ms ease,
    background-color 150ms ease,
    box-shadow 150ms ease;
}

.overview-card--link {
  text-decoration: none;
  cursor: pointer;
}

.overview-card--link:hover {
  transform: translateY(-2px);
  background: var(--active-glass);
  border-color: var(--border-strong);
}

.overview-card--link:active {
  transform: scale(0.99);
}

.overview-card__head {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 10px;
}

.overview-card__head h2 {
  flex: 1 1 auto;
  min-width: 0;
  font-size: 14px;
  font-weight: 650;
  color: var(--text);
  overflow-wrap: anywhere;
}

.overview-card__icon {
  display: inline-grid;
  flex: 0 0 32px;
  width: 32px;
  height: 32px;
  place-items: center;
  border-radius: 10px;
  font-size: 16px;
}

.overview-card__icon--primary {
  color: var(--primary);
  background: var(--primary-soft);
}

.overview-card__icon--purple {
  color: var(--purple);
  background: var(--purple-soft);
}

.overview-card__icon--success {
  color: var(--success);
  background: var(--success-soft);
}

.overview-card__cta {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 3px;
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 560;
}

.overview-card--link:hover .overview-card__cta {
  color: var(--primary);
}

.overview-card__metric {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 4px 6px;
}

.overview-card__metric strong {
  font-size: 30px;
  font-weight: 750;
  line-height: 1.1;
  color: var(--text);
  font-variant-numeric: tabular-nums;
}

.overview-card__metric--compact strong {
  font-size: 24px;
}

.overview-card__denominator {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
}

.overview-card__unit {
  margin-left: 2px;
  font-size: 12px;
  color: var(--text-secondary);
}

.overview-badge-list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: auto;
}

.overview-pill {
  display: inline-flex;
  align-items: center;
  padding: 4px 9px;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 560;
  line-height: 1.3;
  color: var(--text-secondary);
  background: var(--surface-subtle);
  border: 1px solid var(--border);
}

.overview-pill--success {
  color: var(--success);
  background: var(--success-soft);
  border-color: transparent;
}

.overview-pill--danger {
  color: var(--danger);
  background: var(--danger-soft);
  border-color: var(--danger-border);
  font-weight: 650;
}

.overview-pill--muted {
  color: var(--text-muted);
}

.overview-mini-stats {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
  margin-top: auto;
}

.overview-mini-stats--two {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.overview-mini-stats > div {
  min-width: 0;
  padding: 9px 10px;
  border-radius: 12px;
  background: var(--surface-subtle);
  border: 1px solid var(--border);
}

.overview-mini-stats dt {
  color: var(--text-muted);
  font-size: 11px;
  line-height: 1.35;
}

.overview-mini-stats dd {
  margin-top: 4px;
  font-size: 15px;
  font-weight: 700;
  color: var(--text);
  font-variant-numeric: tabular-nums;
}

.overview-mini-stats__highlight {
  background: var(--primary-soft) !important;
  border-color: transparent !important;
}

.overview-mini-stats__highlight dt {
  color: var(--primary);
}

.overview-mini-stats__highlight dd {
  color: var(--primary-dark);
}

.overview-card__footer {
  display: flex;
  justify-content: flex-end;
  margin-top: auto;
  padding-top: 2px;
}

.radar-panel {
  background: var(--glass-bg);
  border: 1px solid var(--glass-border);
  border-radius: 20px;
  box-shadow: var(--glass-shadow);
  -webkit-backdrop-filter: blur(20px) saturate(150%);
  backdrop-filter: blur(20px) saturate(150%);
}

.radar-panel__header {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px 18px;
}

.radar-panel__title-group {
  display: flex;
  min-width: 0;
  align-items: flex-start;
  gap: 12px;
}

.radar-panel__alert-icon {
  display: inline-grid;
  flex: 0 0 36px;
  width: 36px;
  height: 36px;
  place-items: center;
  border-radius: 12px;
  color: var(--danger);
  background: var(--danger-soft);
  font-size: 18px;
}

.radar-panel__title-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.radar-panel__title-row h3 {
  font-size: 16px;
  font-weight: 700;
  color: var(--text);
}

.radar-panel__title-group p {
  margin-top: 4px;
  font-size: 13px;
  line-height: 1.5;
  color: var(--text-secondary);
}

.radar-panel__timestamp {
  flex: 0 0 auto;
  font-size: 12px;
  color: var(--text-muted);
}

.attention-list {
  display: grid;
  gap: 10px;
  padding: 0;
  margin: 0;
  list-style: none;
}

.attention-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  padding: 14px 16px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 14px;
}

.attention-item__main {
  min-width: 0;
  flex: 1 1 auto;
}

.attention-item__identity {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.attention-item__identity strong {
  font-size: 14px;
  font-weight: 650;
  color: var(--text);
}

.attention-item__detail {
  margin-top: 6px;
  font-size: 13px;
  line-height: 1.5;
  color: var(--text-secondary);
  overflow-wrap: anywhere;
}

.attention-item__action {
  flex: 0 0 auto;
}

.health-banner {
  padding: 14px 16px;
  border-radius: 14px;
  background: var(--success-soft);
  border: 1px solid color-mix(in srgb, var(--success) 24%, transparent);
}

.health-banner__content {
  display: flex;
  align-items: flex-start;
  gap: 12px;
}

.health-banner__icon {
  display: inline-grid;
  flex: 0 0 32px;
  width: 32px;
  height: 32px;
  place-items: center;
  border-radius: 10px;
  color: var(--success);
  background: var(--surface);
  font-size: 18px;
}

.health-banner h3 {
  font-size: 15px;
  font-weight: 700;
  color: var(--success);
}

.health-banner p {
  margin-top: 4px;
  font-size: 13px;
  line-height: 1.5;
  color: var(--text-secondary);
}

.workflow-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
}

.workflow-card {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 12px;
  padding: 14px;
  color: var(--text);
  text-decoration: none;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 14px;
  transition:
    transform 150ms ease,
    border-color 150ms ease,
    background-color 150ms ease;
}

.workflow-card:hover {
  transform: translateY(-1px);
  background: var(--row-hover);
  border-color: var(--border-strong);
}

.workflow-card__icon {
  display: inline-grid;
  flex: 0 0 36px;
  width: 36px;
  height: 36px;
  place-items: center;
  border-radius: 10px;
  font-size: 17px;
}

.workflow-card__icon--primary {
  color: var(--primary);
  background: var(--primary-soft);
}

.workflow-card__icon--purple {
  color: var(--purple);
  background: var(--purple-soft);
}

.workflow-card__icon--success {
  color: var(--success);
  background: var(--success-soft);
}

.workflow-card__copy {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 3px;
}

.workflow-card__copy strong {
  font-size: 13px;
  font-weight: 650;
  color: var(--text);
}

.workflow-card__copy small {
  font-size: 12px;
  line-height: 1.4;
  color: var(--text-secondary);
}

.workflow-card__arrow {
  flex: 0 0 auto;
  color: var(--text-muted);
}

.workflow-card:hover .workflow-card__arrow {
  color: var(--primary);
}

@media (max-width: 1024px) {
  .dashboard-kpi-grid,
  .workflow-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 640px) {
  .dashboard-kpi-grid,
  .workflow-grid {
    grid-template-columns: minmax(0, 1fr);
  }

  .overview-card {
    padding: 15px;
    border-radius: 16px;
  }

  .overview-mini-stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .attention-item {
    align-items: stretch;
    flex-direction: column;
  }

  .attention-item__action .el-button {
    width: 100%;
  }
}
</style>
