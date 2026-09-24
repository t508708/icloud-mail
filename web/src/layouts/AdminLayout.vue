<template>
  <div class="admin-shell">
    <a class="skip-link" href="#main-content">跳转到内容</a>
    <aside class="admin-sidebar" aria-label="后台导航">
      <AppBrand />
      <AdminNavigation />
      <SidebarFooter />
    </aside>

    <el-drawer
      v-model="drawerOpen"
      class="mobile-drawer"
      direction="ltr"
      size="min(84vw, 280px)"
      :with-header="false"
      title="后台导航"
      append-to-body
    >
      <div class="mobile-drawer__inner" aria-label="后台导航">
        <AppBrand />
        <AdminNavigation @navigate="drawerOpen = false" />
        <SidebarFooter />
      </div>
    </el-drawer>

    <div class="admin-main">
      <header class="admin-topbar">
        <el-button
          class="admin-menu-button"
          :icon="Menu"
          aria-label="打开导航"
          :aria-expanded="drawerOpen"
          circle
          @click="drawerOpen = true"
        />
        <div class="admin-topbar__copy">
          <span class="admin-topbar__eyebrow">工作空间</span>
          <h1>{{ page.title }}</h1>
        </div>
        <div class="admin-topbar__tools">
          <ThemeControl />
          <div class="admin-topbar__account"><span class="admin-avatar" aria-hidden="true">{{ auth.state.username.slice(0, 1).toUpperCase() }}</span><span class="admin-topbar__username">{{ auth.state.username }}</span></div>
        </div>
      </header>
      <main class="admin-content" id="main-content">
        <router-view />
      </main>
    </div>
  </div>
</template>

<script setup>
import {
  ChatDotRound,
  Connection,
  Document,
  Lock,
  Menu,
  Message,
  SwitchButton,
  Tickets,
} from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import { computed, defineComponent, h, ref, watch } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";

import AppBrand from "../components/AppBrand.vue";
import ThemeControl from "../components/ThemeControl.vue";
import { useAuth } from "../stores/auth.js";
import { usePageHeader } from "../stores/page.js";
import { getActiveAdminSection } from "../utils/adminNavigation.js";
import { showRequestError } from "../utils/feedback.js";
import { preloadRoute } from "../utils/routePreload.js";

const route = useRoute();
const router = useRouter();
const auth = useAuth();
const page = usePageHeader();
const drawerOpen = ref(false);
const logoutLoading = ref(false);
const qqGroupURL = "https://qun.qq.com/#/handy-tool/join-group?groupcode=1105888476";

const menuItems = [
  { to: { name: "accounts" }, label: "主号管理", icon: Connection, section: "accounts" },
  { to: { name: "aliases" }, label: "隐私邮箱", icon: Message, section: "aliases" },
  { to: { name: "pool" }, label: "邮箱池", icon: Tickets, section: "pool" },
  { to: { name: "audit" }, label: "操作记录", icon: Document, section: "audit" },
  { to: { name: "logs" }, label: "全部日志", icon: Tickets, section: "logs" },
  { to: { name: "security" }, label: "安全设置", icon: Lock, section: "security" },
];

const activeSection = computed(() => getActiveAdminSection(route.path));

const AdminNavigation = defineComponent({
  emits: ["navigate"],
  setup(_, { emit }) {
    return () =>
      h(
        "nav",
        { class: "admin-nav", "aria-label": "主要导航" },
        menuItems.flatMap((item, index) => [
          ...(index === 0 || index === 3 ? [h("span", { class: "admin-nav__group" }, index === 0 ? "邮箱工作区" : "管理")] : []),
          h(
            RouterLink,
            {
              to: item.to,
              class: ["admin-nav__item", { "is-active": activeSection.value === item.section }],
              "aria-current": activeSection.value === item.section ? "page" : undefined,
              onClick: () => emit("navigate"),
              onMouseenter: () => preloadRoute(router.resolve(item.to)),
              onFocus: () => preloadRoute(router.resolve(item.to)),
            },
            {
              default: () => [h("span", { class: ["admin-nav__icon", `admin-nav__icon--${item.section}`], "aria-hidden": "true" }, [h(item.icon)]), h("span", item.label)],
            },
          ),
        ]),
      );
  },
});

const SidebarFooter = defineComponent({
  setup() {
    const performLogout = async () => {
      if (logoutLoading.value) return;
      logoutLoading.value = true;
      try {
        await auth.logout();
        ElMessage.closeAll();
        await router.replace({ name: "login" });
      } catch (error) {
        showRequestError(error, "退出失败，请稍后重试。");
      } finally {
        logoutLoading.value = false;
      }
    };
    return () =>
      h("div", { class: "admin-sidebar__footer" }, [
        h(
          "a",
          {
            class: "admin-sidebar__qq",
            href: qqGroupURL,
            target: "_blank",
            rel: "noopener noreferrer",
            title: "加入 QQ 群 1105888476",
          },
          [h(ChatDotRound, { "aria-hidden": "true" }), h("span", "加入 QQ 群")],
        ),
        h("div", { class: "admin-sidebar__footer-main" }, [
        h("span", { class: "admin-sidebar__username", title: auth.state.username }, auth.state.username),
        h(
          "button",
          {
            type: "button",
            class: "quiet-action",
            disabled: logoutLoading.value,
            onClick: performLogout,
          },
          [h(SwitchButton, { "aria-hidden": "true" }), h("span", logoutLoading.value ? "退出中" : "退出")],
        ),
        ]),
      ]);
  },
});

watch(
  () => route.fullPath,
  () => {
    drawerOpen.value = false;
  },
);
</script>
