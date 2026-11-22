<template>
  <section
    class="page-stack virtual-list-page"
    aria-labelledby="audit-section-title"
  >
    <SectionHeader
      id="audit-section-title"
      title="操作记录"
      description="记录后台登录与配置变更，不包含密码、完整 Key 或邮件内容。"
    >
      <template #actions>
        <el-tooltip content="刷新操作记录" placement="bottom">
          <el-button
            :icon="Refresh"
            circle
            :loading="loading"
            aria-label="刷新操作记录"
            @click="loadAuditLogs"
          />
        </el-tooltip>
      </template>
    </SectionHeader>

    <div class="audit-filter-bar" role="search" aria-label="筛选操作记录">
      <el-select v-model="resourceFilter" clearable placeholder="全部对象" aria-label="按对象筛选" @change="applyAuditFilters">
        <el-option v-for="(label, key) in resourceLabels" :key="key" :label="label" :value="key" />
      </el-select>
      <el-select v-model="resultFilter" clearable placeholder="全部结果" aria-label="按执行结果筛选" @change="applyAuditFilters">
        <el-option label="成功" value="success" />
        <el-option label="失败" value="failed" />
      </el-select>
      <el-button v-if="resourceFilter || resultFilter" @click="resetAuditFilters">重置筛选</el-button>
    </div>

    <div v-if="loading && logs.length === 0" class="data-panel loading-panel">
      <el-skeleton :rows="7" animated role="status" aria-live="polite" aria-busy="true" aria-label="正在加载审计记录" />
    </div>

    <div v-else-if="loadError && logs.length === 0" class="load-failed">
      <RequestAlert :error="loadError" />
      <el-button :icon="Refresh" @click="loadAuditLogs">重新加载</el-button>
    </div>

    <EmptyState
      v-else-if="logs.length === 0"
      :title="resourceFilter || resultFilter ? '没有匹配的操作记录' : '暂无操作记录'"
      :description="resourceFilter || resultFilter ? '请调整对象或结果筛选条件后重试。' : '新的登录与配置操作会显示在这里。'"
    >
      <el-button v-if="resourceFilter || resultFilter" @click="resetAuditFilters">重置筛选</el-button>
    </EmptyState>

    <template v-else>
      <RequestAlert
        v-if="loadError"
        :error="loadError"
        closable
        @close="loadError = null"
      />

      <div
        class="data-panel desktop-data-table virtual-list-table"
        :class="{
          'desktop-data-table--force': pageSize > 100 || pageSize === ALL_PAGE_SIZE,
        }"
        :aria-busy="loading"
      >
        <VirtualDataTable
          :columns="auditColumns"
          :data="logs"
          row-key="id"
          fill-height
          :loading="loading"
        >
          <template #cell="{ column, row }">
            <template v-if="column.key === 'createdAt'">
              {{ formatTime(row.createdAt, { seconds: true }) }}
            </template>
            <template v-else-if="column.key === 'username'">
              {{ row.username || "系统" }}
            </template>
            <template v-else-if="column.key === 'action'">
              <strong class="audit-action" :title="row.action">{{ actionLabel(row.action) }}</strong>
            </template>
            <template v-else-if="column.key === 'resource'">
              {{ resourceLabel(row.resourceType, row.resourceId) }}
            </template>
            <template v-else-if="column.key === 'result'">
              <el-tag
                class="audit-result"
                :type="resultMeta(row.result).type"
                effect="plain"
                size="small"
              >
                {{ resultMeta(row.result).label }}
              </el-tag>
            </template>
            <template v-else-if="column.key === 'requestId'">
              <code class="audit-request-id">{{ row.requestId || "-" }}</code>
            </template>
          </template>
        </VirtualDataTable>
      </div>

      <div v-if="pageSize <= 100" class="mobile-record-list" :aria-busy="loading">
        <article v-for="log in logs" :key="log.id" class="mobile-record">
          <header class="mobile-record__header">
            <div class="primary-stack">
              <strong :title="log.action">{{ actionLabel(log.action) }}</strong>
              <small>{{ formatTime(log.createdAt, { seconds: true }) }}</small>
            </div>
            <el-tag
              class="audit-result"
              :type="resultMeta(log.result).type"
              effect="plain"
              size="small"
            >
              {{ resultMeta(log.result).label }}
            </el-tag>
          </header>
          <dl class="mobile-kv-list">
            <div>
              <dt>管理员</dt>
              <dd>{{ log.username || "系统" }}</dd>
            </div>
            <div>
              <dt>对象</dt>
              <dd>{{ resourceLabel(log.resourceType, log.resourceId) }}</dd>
            </div>
            <div>
              <dt>请求编号</dt>
              <dd><code class="audit-request-id">{{ log.requestId || "-" }}</code></dd>
            </div>
          </dl>
        </article>
      </div>

      <ListPagination
        :page="currentPage"
        :page-size="pageSize"
        :total="total"
        :loading="loading"
        aria-label="操作记录分页"
        @change="handlePageChange"
        @size-change="handlePageSizeChange"
      />
    </template>
  </section>
</template>

<script setup>
import { Refresh } from "@element-plus/icons-vue";
import { onBeforeUnmount, onMounted, ref } from "vue";

import { getAllAuditLogs, getAuditLogs } from "../api/admin.js";
import EmptyState from "../components/EmptyState.vue";
import ListPagination from "../components/ListPagination.vue";
import RequestAlert from "../components/RequestAlert.vue";
import SectionHeader from "../components/SectionHeader.vue";
import VirtualDataTable from "../components/VirtualDataTable.vue";
import { createLatestRequestGate } from "../utils/asyncState.js";
import { formatTime } from "../utils/format.js";
import { auditActionLabel } from "../utils/audit.js";
import {
  ALL_PAGE_SIZE,
  DEFAULT_PAGE_SIZE,
  normalizePageSize,
} from "../utils/pagination.js";

const resourceLabels = {
  admin: "管理员",
  account: "主号",
  alias: "隐私邮箱",
  mail_group: "邮箱分组",
  pool: "邮箱池",
  pool_client: "邮箱池项目",
  pool_request: "邮箱领取请求",
  pool_lease: "邮箱租约",
};

const resultLabels = {
  success: { label: "成功", type: "success" },
  degraded: { label: "降级完成", type: "warning" },
  failed: { label: "失败", type: "danger" },
};

const pageSize = ref(DEFAULT_PAGE_SIZE);
const auditColumns = [
  { key: "createdAt", title: "时间", width: 176, flexGrow: 1 },
  { key: "username", title: "管理员", width: 130, flexGrow: 1 },
  { key: "action", title: "操作", width: 142, flexGrow: 1 },
  { key: "resource", title: "对象", width: 150, flexGrow: 1 },
  { key: "result", title: "结果", width: 92 },
  { key: "requestId", title: "请求编号", width: 180, flexGrow: 1 },
];
const logs = ref([]);
const currentPage = ref(1);
const total = ref(0);
const resourceFilter = ref("");
const resultFilter = ref("");
const loading = ref(false);
const loadError = ref(null);
const loadGate = createLatestRequestGate();
let listAbortController = null;
let viewActive = true;

function applyAuditFilters() {
  currentPage.value = 1;
  logs.value = [];
  total.value = 0;
  void loadAuditLogs();
}

function resetAuditFilters() {
  resourceFilter.value = "";
  resultFilter.value = "";
  applyAuditFilters();
}

function actionLabel(action) {
  return auditActionLabel(action);
}

function resourceLabel(type, id) {
  const typeLabel = Object.hasOwn(resourceLabels, type) ? resourceLabels[type] : type || "系统";
  return id ? `${typeLabel} #${id}` : typeLabel;
}

function resultMeta(result) {
  return resultLabels[result] || {
    label: result || "未知",
    type: "info",
  };
}

async function loadAuditLogs() {
  const page = currentPage.value;
  const selectedPageSize = pageSize.value;
  const filterKey = `${resourceFilter.value}\u0000${resultFilter.value}`;
  const requestKey = `${filterKey}\u0000${page}\u0000${selectedPageSize}`;
  const ticket = loadGate.begin(requestKey);
  listAbortController?.abort();
  const abortController = new AbortController();
  listAbortController = abortController;
  loading.value = true;
  loadError.value = null;
  try {
    const filterOpts = {
      resourceType: resourceFilter.value,
      result: resultFilter.value,
      signal: abortController.signal,
    };
    const result = selectedPageSize === ALL_PAGE_SIZE
      ? { items: await getAllAuditLogs(filterOpts) }
      : await getAuditLogs({
          limit: selectedPageSize,
          offset: (page - 1) * selectedPageSize,
          ...filterOpts,
        });
    if (
      !viewActive ||
      !loadGate.isCurrent(ticket, `${resourceFilter.value}\u0000${resultFilter.value}\u0000${currentPage.value}\u0000${pageSize.value}`)
    ) return;
    const nextTotal = Math.max(0, Number(result?.total) || 0);
    const nextItems = Array.isArray(result?.items) ? result.items : [];
    const allItems = selectedPageSize === ALL_PAGE_SIZE;
    const resolvedTotal = allItems ? nextItems.length : nextTotal;
    const lastPage = allItems
      ? 1
      : Math.max(1, Math.ceil(resolvedTotal / selectedPageSize));
    if (!allItems && page > lastPage) {
      currentPage.value = lastPage;
      logs.value = [];
      total.value = resolvedTotal;
      void loadAuditLogs();
      return;
    }
    logs.value = nextItems;
    total.value = resolvedTotal;
  } catch (error) {
    if (
      error?.name !== "AbortError" &&
      viewActive &&
      loadGate.isCurrent(ticket, `${resourceFilter.value}\u0000${resultFilter.value}\u0000${currentPage.value}\u0000${pageSize.value}`)
    ) {
      loadError.value = error;
    }
  } finally {
    if (
      listAbortController === abortController &&
      loadGate.isCurrent(ticket, `${resourceFilter.value}\u0000${resultFilter.value}\u0000${currentPage.value}\u0000${pageSize.value}`)
    ) {
      loading.value = false;
    }
    if (listAbortController === abortController) listAbortController = null;
  }
}

function handlePageChange(page) {
  if (pageSize.value === ALL_PAGE_SIZE) return;
  const nextPage = Math.max(1, Number(page) || 1);
  if (nextPage === currentPage.value) return;
  currentPage.value = nextPage;
  logs.value = [];
  loadError.value = null;
  void loadAuditLogs();
}

function handlePageSizeChange(value) {
  const nextPageSize = normalizePageSize(value);
  if (nextPageSize === pageSize.value) return;
  pageSize.value = nextPageSize;
  currentPage.value = 1;
  logs.value = [];
  total.value = 0;
  loadError.value = null;
  loadGate.invalidate();
  void loadAuditLogs();
}

onMounted(loadAuditLogs);

onBeforeUnmount(() => {
  viewActive = false;
  loadGate.deactivate();
  listAbortController?.abort();
});
</script>

<style scoped>
.audit-action {
  color: var(--el-text-color-primary);
  font-weight: 600;
}

.audit-result {
  min-width: 48px;
  justify-content: center;
}

.audit-request-id {
  overflow-wrap: anywhere;
}

.audit-filter-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  padding: 14px 16px;
  background: var(--glass-bg);
  border: 1px solid var(--glass-border);
  border-radius: 16px;
}

.audit-filter-bar .el-select {
  width: 180px;
}
</style>
