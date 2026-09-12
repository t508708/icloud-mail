<script setup>
import { Check, Monitor, Moon, Sunny } from "@element-plus/icons-vue";
import { useTheme } from "../stores/theme.js";

const { preference, setTheme } = useTheme();
const options = [
  { value: "light", label: "浅色", icon: Sunny },
  { value: "dark", label: "深色", icon: Moon },
  { value: "system", label: "跟随系统", icon: Monitor },
];
const preferenceLabels = { light: "浅色", dark: "深色", system: "跟随系统" };
</script>

<template>
  <el-dropdown trigger="click" placement="bottom-end">
    <button
      class="theme-control"
      type="button"
      aria-label="外观设置"
      :title="`当前外观：${preferenceLabels[preference]}`"
    >
      <Sunny v-if="preference === 'light'" class="theme-control__icon" aria-hidden="true" />
      <Moon v-else-if="preference === 'dark'" class="theme-control__icon" aria-hidden="true" />
      <Monitor v-else class="theme-control__icon" aria-hidden="true" />
    </button>
    <template #dropdown>
      <el-dropdown-menu aria-label="外观设置">
        <el-dropdown-item
          v-for="option in options"
          :key="option.value"
          :aria-current="preference === option.value ? 'true' : undefined"
          @click="setTheme(option.value)"
        >
          <component :is="option.icon" class="theme-control__icon" aria-hidden="true" />
          <span>{{ option.label }}</span>
          <Check v-if="preference === option.value" class="theme-control__icon theme-control__check" aria-hidden="true" />
        </el-dropdown-item>
      </el-dropdown-menu>
    </template>
  </el-dropdown>
</template>

<style scoped>
.theme-control {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  padding: 0;
  border: 1px solid var(--el-border-color);
  border-radius: 50%;
  color: var(--el-text-color-primary);
  background: var(--el-fill-color-blank);
  cursor: pointer;
  transition: background-color 160ms ease, border-color 160ms ease, transform 100ms ease;
}
.theme-control:hover { background: var(--el-fill-color-light); }
.theme-control:active { transform: scale(.94); }
.theme-control:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
.theme-control__icon { width: 18px; height: 18px; flex: 0 0 18px; }
.theme-control__check { margin-left: auto; }
@media (prefers-reduced-motion: reduce) {
  .theme-control { transition: none; }
}
</style>
