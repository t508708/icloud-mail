<template>
  <section class="page-stack" aria-labelledby="inbox-page-title">
    <SectionHeader
      id="inbox-page-title"
      title="收件查看"
      description="输入或选择任意隐藏邮箱（支持带 +tag 子地址），实时同步并查看最近 5 封邮件正文与验证码。"
    />

    <section class="section-block inbox-workbench" aria-label="收件邮箱检索工作台">
      <div class="inbox-search-row">
        <el-autocomplete
          v-model="searchDraft"
          class="inbox-search-input"
          :fetch-suggestions="fetchMailboxSuggestions"
          :debounce="220"
          :trigger-on-focus="true"
          value-key="address"
          clearable
          placeholder="输入隐藏邮箱地址或粘贴带 +tag 的地址（如 foo+chatgpt@icloud.com）"
          aria-label="搜索或输入隐藏邮箱地址"
          @input="handleSearchInput"
          @select="handleSuggestionSelect"
          @keyup.enter="submitLookup"
        >
          <template #prefix>
            <el-icon aria-hidden="true"><Search /></el-icon>
          </template>
          <template #default="{ item }">
            <div class="inbox-suggest-item">
              <div class="inbox-suggest-item__main">
                <strong>{{ item.address }}</strong>
                <el-tag v-if="item.label" size="small" type="info" effect="plain">
                  {{ item.label }}
                </el-tag>
              </div>
              <div class="inbox-suggest-item__meta">
                <span>{{ item.account_email || "未关联主号" }}</span>
                <span v-if="item.latest_received_at">
                  {{ formatRelativeMailTime(item.latest_received_at) }}
                </span>
              </div>
            </div>
          </template>
        </el-autocomplete>

        <el-button
          type="primary"
          :icon="Search"
          :loading="loading && !alias"
          @click="submitLookup"
        >
          查询收件
        </el-button>
      </div>

      <div v-if="recentLookups.length" class="inbox-recent-bar" aria-label="最近查看邮箱">
        <span class="inbox-recent-bar__title">
          <el-icon aria-hidden="true"><Clock /></el-icon>
          最近查看
        </span>
        <div class="inbox-recent-chips">
          <button
            v-for="item in recentLookups"
            :key="`${item.address}#${item.tag || ''}`"
            type="button"
            class="inbox-recent-chip"
            :class="{
              'is-active':
                alias &&
                alias.address?.toLowerCase() === item.address &&
                (activeTag || '').toLowerCase() === (item.tag || ''),
            }"
            @click="selectRecentLookup(item)"
          >
            <span>{{ item.tag ? `${item.address.split('@')[0]}+${item.tag}@${item.address.split('@')[1]}` : item.address }}</span>
            <small v-if="item.label">{{ item.label }}</small>
          </button>
        </div>
        <el-button
          size="small"
          text
          :icon="Delete"
          @click="handleClearRecent"
        >
          清空历史
        </el-button>
      </div>
    </section>

    <RequestAlert
      v-if="loadError"
      :error="loadError"
      closable
      @close="loadError = null"
    />

    <MailboxMessageViewer
      v-if="alias || loading"
      :alias="alias"
      :messages="messages"
      :total-archived="totalArchived"
      :sync-warning="syncWarning"
      :loading="loading"
      :syncing="syncing"
      :active-tag="activeTag"
      :remote-images="remoteImages"
      @sync="handleSync"
      @clear-tag="handleClearTag"
      @toggle-remote-images="handleToggleRemoteImages"
      @copy-share="handleCopyShare"
      @copy-otp-link="handleCopyOtpLink"
      @open-account="handleOpenAccount"
    />

    <EmptyState
      v-else
      title="选择一个隐藏邮箱开始查件"
      description="支持输入隐藏邮箱地址实时联想，也可直接粘贴带 +tag 的子地址（如 alias+chatgpt@icloud.com）自动定位并过滤标签；或从「隐私邮箱」「邮箱池」列表中点击“邮件”直达。"
    >
      <div class="button-row">
        <el-button type="primary" @click="router.push({ name: 'aliases' })">
          前往隐私邮箱列表
        </el-button>
        <el-button @click="router.push({ name: 'pool' })">
          前往邮箱池
        </el-button>
      </div>
    </EmptyState>
  </section>
</template>

<script setup>
import { onBeforeUnmount, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Clock, Delete, Search } from "@element-plus/icons-vue";

import EmptyState from "../components/EmptyState.vue";
import MailboxMessageViewer from "../components/MailboxMessageViewer.vue";
import RequestAlert from "../components/RequestAlert.vue";
import SectionHeader from "../components/SectionHeader.vue";
import { apiRequest } from "../api/client.js";
import { useAuth } from "../stores/auth.js";
import { createLatestRequestGate } from "../utils/asyncState.js";
import { buildOTPDirectLink, copyText } from "../utils/clipboard.js";
import { showRequestError, successMessage } from "../utils/feedback.js";
import {
  buildInboxShareLink,
  clearRecentInboxLookups,
  formatRelativeMailTime,
  loadRecentInboxLookups,
  parsePlusTagAddress,
  saveRecentInboxLookup,
} from "../utils/inbox.js";

const route = useRoute();
const router = useRouter();
const auth = useAuth();

const searchDraft = ref("");
const lastTypedQuery = ref("");
const skipNextEnterLookup = ref(false);
const recentLookups = ref(loadRecentInboxLookups());
const alias = ref(null);
const messages = ref([]);
const totalArchived = ref(0);
const syncWarning = ref("");
const activeTag = ref("");
const remoteImages = ref(false);
const loading = ref(false);
const syncing = ref(false);
const loadError = ref(null);

const mailboxGate = createLatestRequestGate();
let mailboxAbortController = null;
let suggestAbortController = null;
let syncingRouteQuery = false;

function abortMailboxRequest() {
  if (mailboxAbortController) {
    mailboxAbortController.abort();
    mailboxAbortController = null;
  }
}

function abortSuggestRequest() {
  if (suggestAbortController) {
    suggestAbortController.abort();
    suggestAbortController = null;
  }
}

onBeforeUnmount(() => {
  abortMailboxRequest();
  abortSuggestRequest();
  mailboxGate.deactivate();
});

async function fetchMailboxSuggestions(queryString, callback) {
  abortSuggestRequest();
  suggestAbortController = new AbortController();
  const signal = suggestAbortController.signal;
  const trimmed = String(queryString || "").trim();

  try {
    const data = await apiRequest(
      `/inbox/suggest?q=${encodeURIComponent(trimmed)}&limit=12`,
      { signal },
    );
    const items = Array.isArray(data?.items) ? data.items : [];
    callback(items);
  } catch (error) {
    if (error?.name !== "AbortError") {
      callback([]);
    }
  }
}

function recordRecent(resolvedAlias, tag = "") {
  if (!resolvedAlias?.address) return;
  recentLookups.value = saveRecentInboxLookup({
    id: resolvedAlias.id,
    address: resolvedAlias.address,
    label: resolvedAlias.label || "",
    accountEmail: resolvedAlias.account_email || resolvedAlias.accountEmail || "",
    tag,
    viewedAt: new Date().toISOString(),
  });
}

async function syncRouteQuery(resolvedAlias, tag = "") {
  const nextQuery = {};
  if (resolvedAlias?.id) nextQuery.alias_id = String(resolvedAlias.id);
  if (resolvedAlias?.address) nextQuery.address = resolvedAlias.address;
  if (tag) nextQuery.tag = tag;

  const sameId = String(route.query.alias_id || "") === String(nextQuery.alias_id || "");
  const sameAddr = String(route.query.address || "") === String(nextQuery.address || "");
  const sameTag = String(route.query.tag || "") === String(nextQuery.tag || "");
  if (sameId && sameAddr && sameTag) return;

  syncingRouteQuery = true;
  try {
    await router.replace({ query: nextQuery });
  } finally {
    syncingRouteQuery = false;
  }
}

function applyMessagesPayload(data) {
  if (data?.alias) {
    alias.value = data.alias;
  }
  messages.value = Array.isArray(data?.messages) ? data.messages : [];
  totalArchived.value = Number(data?.total_archived) || 0;
  syncWarning.value = data?.sync_warning || "";
}

async function loadMailbox({ aliasId = null, address = "", tag = "", updateRoute = true } = {}) {
  const parsed = parsePlusTagAddress(address);
  const numericId = Number(aliasId);
  const hasAliasId = Number.isFinite(numericId) && numericId > 0;
  if (!hasAliasId && !parsed.normalized) {
    return;
  }

  abortMailboxRequest();
  mailboxAbortController = new AbortController();
  const signal = mailboxAbortController.signal;
  const ticket = mailboxGate.begin(`${aliasId || ""}:${parsed.normalized}:${tag}`);

  loading.value = true;
  loadError.value = null;

  try {
    let targetId = hasAliasId ? Math.trunc(numericId) : null;
    let resolvedTag = String(tag || parsed.tag || "").trim();

    if (!targetId) {
      const lookup = await apiRequest(
        `/inbox/lookup?address=${encodeURIComponent(parsed.normalized)}`,
        { signal },
      );
      if (!mailboxGate.isCurrent(ticket)) return;
      if (lookup?.alias) {
        alias.value = lookup.alias;
        targetId = lookup.alias.id;
      }
      if (!tag && (lookup?.matched_tag || parsed.tag)) {
        resolvedTag = lookup?.matched_tag || parsed.tag;
      }
    }

    if (!targetId) {
      throw new Error("未找到匹配的隐藏邮箱。");
    }

    activeTag.value = resolvedTag;

    const params = new URLSearchParams({
      limit: "5",
      remote_images: remoteImages.value ? "1" : "0",
    });
    if (resolvedTag) {
      params.set("tag", resolvedTag);
    }

    const data = await apiRequest(
      `/aliases/${encodeURIComponent(targetId)}/messages?${params.toString()}`,
      { signal },
    );
    if (!mailboxGate.isCurrent(ticket)) return;

    applyMessagesPayload(data);

    const resolvedAlias = data?.alias || alias.value;
    if (resolvedAlias?.address) {
      searchDraft.value = resolvedTag
        ? `${resolvedAlias.address.split("@")[0]}+${resolvedTag}@${resolvedAlias.address.split("@")[1]}`
        : resolvedAlias.address;
      recordRecent(resolvedAlias, resolvedTag);
    }
    if (updateRoute && resolvedAlias) {
      await syncRouteQuery(resolvedAlias, resolvedTag);
    }
  } catch (error) {
    if (error?.name === "AbortError" || !mailboxGate.isCurrent(ticket)) {
      return;
    }
    loadError.value = error;
  } finally {
    if (mailboxGate.isCurrent(ticket)) {
      loading.value = false;
    }
  }
}

watch(
  () => [route.query.alias_id, route.query.address, route.query.tag],
  ([aliasId, address, tag]) => {
    if (syncingRouteQuery) return;
    const rawAddress = String(address || "").trim();
    const rawTag = String(tag || "").trim();
    const hasId = Boolean(aliasId && Number(aliasId) > 0);
    if (!hasId && !rawAddress) return;
    remoteImages.value = false;
    loadMailbox({
      aliasId: hasId ? Number(aliasId) : null,
      address: rawAddress,
      tag: rawTag,
      updateRoute: false,
    });
  },
  { immediate: true },
);

function handleSearchInput(value) {
  lastTypedQuery.value = String(value ?? "");
}

function submitLookup() {
  if (skipNextEnterLookup.value) {
    skipNextEnterLookup.value = false;
    return;
  }
  const trimmed = String(searchDraft.value || "").trim();
  if (!trimmed) return;
  remoteImages.value = false;
  loadMailbox({ address: trimmed });
}

function handleSuggestionSelect(item) {
  if (!item) return;
  skipNextEnterLookup.value = true;
  queueMicrotask(() => {
    skipNextEnterLookup.value = false;
  });
  const parsed = parsePlusTagAddress(lastTypedQuery.value || searchDraft.value);
  const tag =
    parsed.rootAddress && parsed.rootAddress === String(item.address || "").toLowerCase()
      ? parsed.tag
      : "";
  remoteImages.value = false;
  loadMailbox({
    aliasId: item.id,
    address: item.address,
    tag,
  });
}

function selectRecentLookup(item) {
  remoteImages.value = false;
  loadMailbox({
    aliasId: item.id,
    address: item.address,
    tag: item.tag || "",
  });
}

function handleClearRecent() {
  recentLookups.value = clearRecentInboxLookups();
}

async function handleSync() {
  const targetId = alias.value?.id;
  if (!targetId || syncing.value) return;

  abortMailboxRequest();
  mailboxAbortController = new AbortController();
  const signal = mailboxAbortController.signal;
  const ticket = mailboxGate.begin(`sync:${targetId}`);

  syncing.value = true;
  loadError.value = null;

  try {
    const data = await apiRequest(`/aliases/${encodeURIComponent(targetId)}/messages/sync`, {
      method: "POST",
      body: {
        limit: 5,
        tag: activeTag.value,
        remote_images: remoteImages.value,
      },
      csrfToken: auth.state.csrfToken,
      signal,
    });
    if (!mailboxGate.isCurrent(ticket)) return;
    applyMessagesPayload(data);
    successMessage("已完成最新邮件同步");
  } catch (error) {
    if (error?.name === "AbortError" || !mailboxGate.isCurrent(ticket)) {
      return;
    }
    showRequestError(error, "同步邮件失败，请稍后重试。");
  } finally {
    if (mailboxGate.isCurrent(ticket)) {
      syncing.value = false;
    }
  }
}

function handleClearTag() {
  if (!alias.value?.id) return;
  loadMailbox({
    aliasId: alias.value.id,
    address: alias.value.address,
    tag: "",
  });
}

function handleToggleRemoteImages() {
  if (!alias.value?.id) return;
  remoteImages.value = !remoteImages.value;
  loadMailbox({
    aliasId: alias.value.id,
    address: alias.value.address,
    tag: activeTag.value,
    updateRoute: false,
  });
}

async function handleCopyShare(targetAlias) {
  const path = targetAlias?.inbox_share_path || targetAlias?.inboxSharePath;
  if (!path) return;
  try {
    const url = buildInboxShareLink(path);
    if (await copyText(url)) {
      successMessage("已复制查件分享链接");
    } else {
      showRequestError(new Error("复制失败，请手动复制。"));
    }
  } catch (error) {
    showRequestError(error);
  }
}

async function handleCopyOtpLink(targetAlias) {
  const path = targetAlias?.otp_url_path || targetAlias?.otpUrlPath;
  if (!path) return;
  try {
    const url = buildOTPDirectLink(path);
    if (await copyText(url)) {
      successMessage("已复制 OTP 接口链接");
    } else {
      showRequestError(new Error("复制失败，请手动复制。"));
    }
  } catch (error) {
    showRequestError(error);
  }
}

function handleOpenAccount(targetAlias) {
  const accountId = targetAlias?.account_id || targetAlias?.accountId;
  if (!accountId) return;
  router.push({ name: "account-detail", params: { id: accountId } });
}
</script>

<style scoped>
.inbox-workbench {
  gap: 12px;
}

.inbox-search-row {
  display: flex;
  align-items: center;
  gap: 10px;
}

.inbox-search-input {
  flex: 1 1 auto;
  min-width: 0;
}

.inbox-suggest-item {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 5px 0;
  line-height: 1.35;
}

.inbox-suggest-item__main {
  display: flex;
  align-items: center;
  gap: 8px;
}

.inbox-suggest-item__main strong {
  font-size: 13px;
  color: var(--text);
}

.inbox-suggest-item__meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  font-size: 11px;
  color: var(--text-muted);
}

.inbox-recent-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 10px;
}

.inbox-recent-bar__title {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 560;
}

.inbox-recent-chips {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  flex: 1 1 auto;
}

.inbox-recent-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-radius: 999px;
  border: 1px solid var(--border);
  background: var(--surface-subtle);
  color: var(--text-secondary);
  font-size: 12px;
  cursor: pointer;
  transition: border-color 120ms ease, background-color 120ms ease, color 120ms ease;
}

.inbox-recent-chip:hover {
  color: var(--text);
  border-color: var(--border-strong);
  background: var(--row-hover);
}

.inbox-recent-chip.is-active {
  color: var(--primary);
  background: var(--primary-soft);
  border-color: color-mix(in srgb, var(--primary) 35%, var(--border));
  font-weight: 600;
}

.inbox-recent-chip small {
  color: var(--text-muted);
  font-size: 11px;
}

@media (max-width: 720px) {
  .inbox-search-row {
    flex-direction: column;
    align-items: stretch;
  }
}
</style>
