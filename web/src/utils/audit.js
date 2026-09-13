// Keep audit action presentation in one place so new backend actions remain
// diagnosable while the common operations are readable in the UI.
export const auditActionLabels = {
  login: "登录后台",
  logout: "退出登录",
  change_password: "修改登录密码",
  create: "创建",
  update: "更新",
  correct_email: "更正主号邮箱",
  delete: "删除",
  sync: "同步主号",
  sync_hme_aliases: "同步隐藏邮箱目录",
  rotate_key: "轮换 API Key",
  rotate_credentials: "轮换整套凭证",
  rotate_all_credentials: "轮换全部凭证",
  toggle: "切换启用状态",
  move_group: "移动邮箱分组",
  create_random: "批量创建随机邮箱",
  alias_create_manual: "手动创建邮箱",
  alias_auto_create_set: "设置自动创建",
  alias_auto_create_keys_read: "读取自动创建密钥",
  alias_auto_create_keys_ack: "确认自动创建密钥",
  alias_creation_job_start: "开始批量创建任务",
  alias_creation_job_stop: "停止批量创建任务",
  apple_auth_start: "登录旧通道",
  apple_auth_verify: "验证旧通道登录",
  apple_account_auth: "登录新通道",
  apple_account_logout: "退出新通道",
  apple_account_verify: "验证新通道登录",
  apple_session_clear: "退出旧通道",
  pool_claim: "领取邮箱池租约",
  pool_release: "释放邮箱池租约",
  pool_commit: "确认使用邮箱池租约",
  pool_renew: "续期邮箱池租约",
  pool_settings: "更新邮箱池设置",
  pool_enroll: "加入邮箱池",
  pool_client_create: "创建邮箱池客户端",
  pool_client_update: "更新邮箱池客户端",
};

export function auditActionLabel(action) {
  return action && Object.hasOwn(auditActionLabels, action)
    ? auditActionLabels[action]
    : action || "未知操作";
}
