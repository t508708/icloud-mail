<template>
  <el-drawer
    :model-value="modelValue"
    size="min(960px, 95vw)"
    destroy-on-close
    class="inbox-drawer"
    :aria-label="drawerTitle"
    @update:model-value="$emit('update:modelValue', $event)"
  >
    <template #header>
      <div class="inbox-drawer__header">
        <span class="inbox-drawer__title">{{ drawerTitle }}</span>
        <el-tag v-if="activeTag" size="small" type="info" effect="plain">
          +{{ activeTag }}
        </el-tag>
      </div>
    </template>

    <div class="inbox-drawer__body">
      <RequestAlert
        v-if="loadError"
        :error="loadError"
        closable
        @close="loadError = null"
      />

      <MailboxMessageViewer
        :alias="alias"
        :messages="messages"
        :total-archived="totalArchived"
        :sync-warning="syncWarning"
        :loading="loading"
        :syncing="syncing"
        :active-tag="activeTag"
        :remote-images="remoteImages"
        compact
        @sync="handleSync"
        @clear-tag="handleClearTag"
        @toggle-remote-images="handleToggleRemoteImages"
        @copy-share="handleCopyShare"
        @copy-otp-link="handleCopyOtpLink"
        @open-full-page="handleOpenFullPage"
        @open-account="handleOpenAccount"
      />
    </div>
  </el-drawer>
</template>

<script setup>
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useRouter } from "vue-router";

import MailboxMessageViewer from "./MailboxMessageViewer.vue";
import RequestAlert from "./RequestAlert.vue";
import { apiRequest } from "../api/client.js";
import { useAuth } from "../stores/auth.js";
import { createLatestRequestGate } from "../utils/asyncState.js";
import { buildOTPDirectLink, copyText } from "../utils/clipboard.js";
import { showRequestError, successMessage } from "../utils/feedback.js";
import { buildInboxShareLink, parsePlusTagAddress } from "../utils/inbox.js";

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  aliasId: { type: [Number, String], default: null },
  address: { type: String, default: "" },
  initialTag: { type: String, default: "" },
});

const emit = defineEmits(["update:modelValue"]);

const router = useRouter();
const auth = useAuth();

const alias = ref(null);
const messages = ref([]);
const totalArchived = ref(0);
const syncWarning = ref("");
const loading = ref(false);
const syncing = ref(false);
const loadError = ref(null);
const activeTag = ref("");
const remoteImages = ref(false);

const requestGate = createLatestRequestGate();
let activeAbortController = null;

const drawerTitle = computed(() => {
  if (alias.value?.address) return `收件查看 · ${alias.value.address}`;
  if (props.address) return `收件查看 · ${props.address}`;
  return "收件查看";
});

function abortInFlight() {
  if (activeAbortController) {
    activeAbortController.abort();
    activeAbortController = null;
  }
}

function resetState() {
  abortInFlight();
  requestGate.invalidate();
  alias.value = null;
  messages.value = [];
  totalArchived.value = 0;
  syncWarning.value = "";
  loadError.value = null;
  loading.value = false;
  syncing.value = false;
  remoteImages.value = false;
}

function applyMessagesPayload(data) {
  if (data?.alias) {
    alias.value = data.alias;
  }
  messages.value = Array.isArray(data?.messages) ? data.messages : [];
  totalArchived.value = Number(data?.total_archived) || 0;
  syncWarning.value = data?.sync_warning || "";
}

async function fetchMessages(resolvedAliasId, { tag = activeTag.value, signal, ticket } = {}) {
  const params = new URLSearchParams({
    limit: "5",
    remote_images: remoteImages.value ? "1" : "0",
  });
  if (tag) {
    params.set("tag", tag);
  }
  const data = await apiRequest(
    `/aliases/${encodeURIComponent(resolvedAliasId)}/messages?${params.toString()}`,
    { signal },
  );
  if (!requestGate.isCurrent(ticket)) return;
  applyMessagesPayload(data);
}

async function loadDrawerInbox() {
  if (!props.modelValue) return;
  const parsed = parsePlusTagAddress(props.address);
  const explicitId = Number(props.aliasId);
  const hasAliasId = Number.isFinite(explicitId) && explicitId > 0;
  if (!hasAliasId && !parsed.normalized) {
    return;
  }

  abortInFlight();
  activeAbortController = new AbortController();
  const signal = activeAbortController.signal;
  const ticket = requestGate.begin(`${props.aliasId || ""}:${parsed.normalized}:${activeTag.value}`);

  loading.value = true;
  loadError.value = null;

  try {
    let targetId = hasAliasId ? Math.trunc(explicitId) : null;
    let resolvedTag = activeTag.value;

    if (!targetId) {
      const lookup = await apiRequest(
        `/inbox/lookup?address=${encodeURIComponent(parsed.normalized)}`,
        { signal },
      );
      if (!requestGate.isCurrent(ticket)) return;
      if (lookup?.alias) {
        alias.value = lookup.alias;
        targetId = lookup.alias.id;
      }
      if (!resolvedTag && (lookup?.matched_tag || parsed.tag)) {
        resolvedTag = lookup?.matched_tag || parsed.tag;
        activeTag.value = resolvedTag;
      }
    }

    if (!targetId) {
      throw new Error("未找到对应的隐藏邮箱。");
    }

    await fetchMessages(targetId, { tag: resolvedTag, signal, ticket });
  } catch (error) {
    if (error?.name === "AbortError" || !requestGate.isCurrent(ticket)) {
      return;
    }
    loadError.value = error;
  } finally {
    if (requestGate.isCurrent(ticket)) {
      loading.value = false;
    }
  }
}

watch(
  () => [props.modelValue, props.aliasId, props.address, props.initialTag],
  ([open, , address, initialTag]) => {
    if (!open) {
      resetState();
      return;
    }
    const parsed = parsePlusTagAddress(address);
    activeTag.value = String(initialTag || parsed.tag || "").trim();
    remoteImages.value = false;
    loadDrawerInbox();
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  abortInFlight();
  requestGate.deactivate();
});

async function handleSync() {
  const targetId = alias.value?.id || props.aliasId;
  if (!targetId || syncing.value) return;

  abortInFlight();
  activeAbortController = new AbortController();
  const signal = activeAbortController.signal;
  const ticket = requestGate.begin(`sync:${targetId}`);

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
    if (!requestGate.isCurrent(ticket)) return;
    applyMessagesPayload(data);
    successMessage("已完成最新邮件同步");
  } catch (error) {
    if (error?.name === "AbortError" || !requestGate.isCurrent(ticket)) {
      return;
    }
    showRequestError(error, "同步邮件失败，请稍后重试。");
  } finally {
    if (requestGate.isCurrent(ticket)) {
      syncing.value = false;
    }
  }
}

function handleClearTag() {
  activeTag.value = "";
  loadDrawerInbox();
}

function handleToggleRemoteImages() {
  remoteImages.value = !remoteImages.value;
  loadDrawerInbox();
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

function handleOpenFullPage(targetAlias) {
  emit("update:modelValue", false);
  const query = {};
  if (targetAlias?.id) query.alias_id = String(targetAlias.id);
  if (targetAlias?.address) query.address = targetAlias.address;
  if (activeTag.value) query.tag = activeTag.value;
  router.push({ name: "inbox", query });
}

function handleOpenAccount(targetAlias) {
  const accountId = targetAlias?.account_id || targetAlias?.accountId;
  if (!accountId) return;
  emit("update:modelValue", false);
  router.push({ name: "account-detail", params: { id: accountId } });
}
</script>

<style scoped>
.inbox-drawer__header {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.inbox-drawer__title {
  font-size: 16px;
  font-weight: 650;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.inbox-drawer__body {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
</style>
