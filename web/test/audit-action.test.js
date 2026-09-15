import test from "node:test";
import assert from "node:assert/strict";

import { auditActionLabel } from "../src/utils/audit.js";

test("audit action labels cover backend actions and preserve unknown codes", () => {
  assert.equal(auditActionLabel("alias_creation_job_start"), "开始批量创建任务");
  assert.equal(auditActionLabel("apple_auth_start"), "登录旧通道");
  assert.equal(auditActionLabel("apple_auth_verify"), "验证旧通道登录");
  assert.equal(auditActionLabel("apple_account_auth"), "登录新通道");
  assert.equal(auditActionLabel("apple_account_logout"), "退出新通道");
  assert.equal(auditActionLabel("correct_email"), "更正主号邮箱");
  assert.equal(auditActionLabel("pool_commit"), "确认使用邮箱池租约");
  assert.equal(auditActionLabel("pool_release"), "释放邮箱池租约");
  assert.equal(auditActionLabel("pool_renew"), "续期邮箱池租约");
  assert.equal(auditActionLabel("future_action"), "future_action");
  assert.equal(auditActionLabel("__proto__"), "__proto__");
  assert.equal(auditActionLabel(""), "未知操作");
});
