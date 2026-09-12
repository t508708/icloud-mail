# 控制台设计约定

## 方向

以 macOS 邮件与 iCloud 设置的工作台为参考：浮动 Liquid Glass 导航与顶栏、紧凑工具栏、可扫描的列表、明确的选中态。真实邮箱与运行状态是内容中心，不增加装饰性仪表盘或伪造统计。

外观：浅色、深色、跟随系统，后台与登录页右上角切换，本地持久化并同步浏览器标签页。颜色集中于 `web/src/styles/theme.css`；组件使用语义 token。玻璃用于导航、顶栏、工具条和弹窗，数据表使用不透明底色。背景仅静态 CSS 色场，短时 hover、按压和切换反馈，不使用持续动画、Canvas 或大面积跟随鼠标的滤镜。

字体优先系统 UI 字体；JS、CSS、图标均随应用本地打包，不调用外部 CDN。主题初始化使用同源 `assets/theme-init-v1.js`，在主包执行前应用偏好，兼容现有 CSP。该文件长期缓存，修改时须递增文件名版本。间距以 4 / 8 / 12 / 16 / 24 为主。

应用响应使用 `Cache-Control: no-store, no-transform`，避免 Cloudflare 自动注入外部统计脚本；静态资源仍长期缓存。依据 [Cloudflare 自动注入说明](https://developers.cloudflare.com/web-analytics/get-started/#sites-proxied-through-cloudflare)，发布验收须在真实浏览器中确认请求来源，源码扫描不足以发现代理层注入。

布局：左侧悬浮工作区导航，右侧玻璃顶栏和内容；标题左对齐，主操作靠右。主号的连接详情、自动创建计划明细折叠展示，状态、异常与常用操作常驻；批量创建与隐私邮箱同级。列表筛选和批量操作各有明确位置。表格保留虚拟滚动，移动端使用可操作的记录卡片。

```
导航          页面位置                         外观 / 当前管理员
              标题                            主操作 / 更多
              搜索 / 筛选
              选择数量 / 本页操作 / 批量操作
              表格
              分页
```

交互：跨页选择保留、筛选切换清选；勾选列拖动增选/取消，修饰键反选，边缘滚动。危险操作仍二次确认。完成任务不常驻。限流归为正常冷却；登录、网络、持久化异常仍清晰报告。尊重 reduced-motion 和 reduced-transparency；缺少 backdrop-filter 支持时使用不透明底色。所有颜色语义、焦点、禁用与 loading 状态须在两种外观下检查。

## 参考与取舍

- [frontend-design](https://github.com/anthropics/skills/tree/main/skills/frontend-design)：设计计划、信息层级、截图审查。
- [Reka UI](https://github.com/unovue/reka-ui)（MIT）：键盘和焦点管理的组件交互参考。
- [shadcn-vue](https://github.com/unovue/shadcn-vue)（MIT）：紧凑工具栏和组合式界面参考；默认分支为 `dev`，旧 `main` 文档不代表现状。
- [Element Plus](https://github.com/element-plus/element-plus)（MIT）：保留项目现有组件与虚拟表格，通过主题和布局统一设计，不额外引入第二套组件框架。

验收覆盖 1440 / 1024 / 390 / 320 宽度、键盘导航、弹窗焦点、跨页选择、拖选自动滚动、请求竞态、静态缓存与首屏资源。
