<template>
  <section
    v-if="visible"
    class="data-panel alias-deletion-progress"
    aria-label="Apple 批量删除任务"
  >
    <div class="alias-deletion-progress__header">
      <strong>Apple 批量删除</strong>
      <el-tag :type="statusType">{{ statusLabel }}</el-tag>
      <el-button
        :loading="state.checking"
        :disabled="state.submitting"
        @click="$emit('refresh')"
      >刷新任务状态</el-button>
      <el-button
        v-if="job?.status === 'completed' || job?.status === 'interrupted'"
        text
        :disabled="state.uncertain"
        aria-label="关闭删除任务提示"
        @click="visibility.dismiss()"
      >关闭</el-button>
    </div>
    <template v-if="job">
      <p role="status" aria-live="polite" aria-atomic="true">
        已处理 {{ job.processed }} / {{ job.requested }}；
        成功删除 {{ job.deleted }}；失败/未执行 {{ job.failed }}
        <span v-if="job.deferred">（其中未执行 {{ job.deferred }}）</span>
      </p>
      <el-progress :percentage="percentage" :show-text="false" aria-label="Apple 批量删除进度" />
      <p v-if="isAliasDeletionJobActive(job)">任务在后台继续执行，离开或刷新页面后可继续查看进度。</p>
      <div v-if="waits.length" class="alias-deletion-progress__waits" role="status">
        <strong>Apple 限流，等待后继续</strong>
        <p v-for="wait in waits" :key="`${wait.accountId}:${wait.aliasId}:${wait.operation}`">
          主号 {{ wait.accountId }} · {{ ALIAS_DELETION_OPERATION_LABELS[wait.operation] || wait.operation }}
          · 预计重试：{{ formatTime(wait.retryAt, { seconds: true }) }}
          · 第 {{ wait.attempt }} / {{ wait.maxAttempts }} 次重试
        </p>
      </div>
    </template>
    <p v-if="state.recovering" role="status">正在查询当前管理员的最近删除任务。</p>
    <p v-if="state.uncertain" role="status">
      删除结果待确认，系统继续查询进度；请核对结果后再操作。
      <span v-if="state.error?.message">{{ state.error.message }}</span>
    </p>
    <p v-if="job?.status === 'interrupted'">
      任务已中断，请刷新 Apple 目录核对剩余邮箱后重新选择。
    </p>
    <div v-if="state.unmatched">
      <p>尚未查到本次提交对应的任务，请先刷新 Apple 目录并核对结果。</p>
      <el-button :disabled="state.checking" @click="$emit('acknowledge')">已刷新 Apple 目录并核对结果</el-button>
    </div>
    <div v-if="recentFailures.length" class="alias-deletion-progress__failures">
      <strong>近期未删除或待确认结果</strong>
      <p v-for="result in recentFailures" :key="result.id">
        {{ result.address || `ID ${result.id}` }}：{{ formatAliasDeletionResultMessage(result) }}
      </p>
    </div>
    <details v-if="job?.results?.length" @toggle="resultsExpanded = $event.target.open">
      <summary>查看逐项结果（{{ job.results.length }} 项）</summary>
      <ul v-if="resultsExpanded" class="alias-deletion-progress__results">
        <li v-for="result in job.results" :key="result.id">
          {{ result.address || `ID ${result.id}` }}：{{ formatAliasDeletionResultMessage(result) }}
        </li>
      </ul>
    </details>
  </section>
</template>

<script setup>
import { computed, onBeforeUnmount, ref, watch } from "vue";
import {
  ALIAS_DELETION_OPERATION_LABELS,
  formatAliasDeletionResultMessage,
  isAliasDeletionJobActive,
} from "../utils/aliasDeletionJob.js";
import { formatTime } from "../utils/format.js";
import { createAliasDeletionVisibility } from "../utils/aliasDeletionVisibility.js";

const props = defineProps({ state: { type: Object, required: true } });
defineEmits(["refresh", "acknowledge"]);
const resultsExpanded = ref(false);
const visible = ref(false);
const visibility = createAliasDeletionVisibility({
  onChange: (value) => { visible.value = value; },
});
const job = computed(() => props.state.job);
const percentage = computed(() => job.value?.requested
  ? Math.min(100, Math.max(0, job.value.processed / job.value.requested * 100)) : 0);
const waits = computed(() => job.value?.status === "running" ? job.value.waits || [] : []);
const recentFailures = computed(() => (job.value?.results || []).filter((result) => !result.deleted).slice(-5));
const statusType = computed(() => props.state.uncertain || waits.value.length || job.value?.failed || job.value?.status === "interrupted"
  ? "warning" : job.value?.status === "completed" ? "success" : "info");
const statusLabel = computed(() => {
  if (props.state.submitting) return "正在提交";
  if (props.state.uncertain) return "结果待确认";
  if (props.state.recovering) return "恢复任务中";
  if (waits.value.length) return "限流等待中";
  return { queued: "排队中", running: "执行中", completed: "已完成", interrupted: "已中断" }[job.value?.status] || "查询中";
});
watch(() => job.value?.jobId, () => {
  resultsExpanded.value = false;
});
watch(() => props.state, (state) => visibility.update(state), { immediate: true, deep: true });
onBeforeUnmount(() => visibility.stop());
</script>

<style scoped>
.alias-deletion-progress { display: grid; gap: 12px; padding: 16px; overflow-wrap: anywhere; }
.alias-deletion-progress__header { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.alias-deletion-progress p { margin: 0; line-height: 1.7; }
.alias-deletion-progress__waits,
.alias-deletion-progress__failures { display: grid; gap: 6px; color: var(--text-secondary); }
.alias-deletion-progress__results { max-height: 280px; overflow: auto; padding-left: 20px; }
.alias-deletion-progress summary { cursor: pointer; }
</style>
