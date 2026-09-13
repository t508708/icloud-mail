import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const source = await readFile(new URL("../src/views/AccountDetailView.vue", import.meta.url), "utf8");

test("directory sync is explicit about not fetching message bodies", () => {
  assert.match(source, /同步地址目录/);
  assert.match(source, /仅同步隐藏邮箱地址、备注与启停状态，不下载邮件正文或验证码/);
  assert.match(source, /本次未读取邮件/);
  assert.doesNotMatch(source, /title="IMAP 邮件同步"/);
  assert.match(source, /title="邮件接收"/);
  assert.match(source, /访问取件地址或取件 API 时才读取对应邮箱/);
});

test("manual whole-account fetch is advanced and authentication failures block it", () => {
  const details = source.match(/<details class="settings-disclosure">([\s\S]*?)<\/details>/)?.[1];
  assert.ok(details);
  assert.match(details, /手动同步主号邮件/);
  assert.match(details, /:disabled="[^"]*isIMAPAuthenticationFailure\(account.lastSyncError\)"/);
  assert.match(source, /async function syncNow\(\) \{\s*if \([^\n]*isIMAPAuthenticationFailure\(account.value.lastSyncError\)\) return/);
  assert.match(source, /配置收件凭据/);
  assert.match(source, /地址目录同步不代表收件恢复/);
});
