<template>
  <div class="app-brand">
    <span class="app-brand__mark" aria-hidden="true">
      <svg width="25" height="25" viewBox="0 0 24 24" fill="none">
        <rect x="3" y="5" width="18" height="14" rx="3" stroke="currentColor" stroke-width="1.6" />
        <path d="m4 7 8 6 8-6" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
    </span>
    <span class="app-brand__copy">
      <strong>{{ title }}</strong>
      <small>{{ subtitle }}</small>
    </span>
  </div>
</template>

<script setup>
defineProps({
  title: { type: String, default: "隐私邮箱" },
  subtitle: { type: String, default: "收件 API 管理" },
});
</script>
