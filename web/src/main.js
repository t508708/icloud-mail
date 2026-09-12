import { createApp } from "vue";
import "element-plus/es/components/message/style/css";
import "element-plus/es/components/message-box/style/css";
import "element-plus/es/components/notification/style/css";
import "element-plus/theme-chalk/dark/css-vars.css";

import App from "./App.vue";
import router from "./router/index.js";
import { initializeTheme } from "./stores/theme.js";
import "./styles/theme.css";
import "./styles/index.css";

initializeTheme();
createApp(App).use(router).mount("#app");
