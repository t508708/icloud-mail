import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  buildProjectMailboxFilename,
  buildProjectMailboxExportText,
  collectProjectMailboxes,
} from "../src/utils/projectMailboxExport.js";

const origin = "https://mail.example.test:8443";

test("shared mailbox TXT uses OTP direct links, five hyphens, and CRLF without a header", () => {
  assert.equal(buildProjectMailboxExportText([
    { alias_id: 1, address: "first@icloud.com", otp_url_path: "/api/v1/otp?token=first" },
    { alias_id: 2, address: "second@icloud.com", otp_url_path: "/api/v1/otp?token=second%2Bvalue" },
  ], origin), [
    "first@icloud.com-----https://mail.example.test:8443/api/v1/otp?token=first",
    "second@icloud.com-----https://mail.example.test:8443/api/v1/otp?token=second%2Bvalue",
  ].join("\r\n"));
  assert.equal(buildProjectMailboxExportText([], origin), "");
});

test("TXT export rejects missing or multiline addresses and invalid OTP paths", () => {
  for (const row of [
    { address: "", otp_url_path: "/api/v1/otp?token=valid" },
    { address: "x@icloud.com\ny@icloud.com", otp_url_path: "/api/v1/otp?token=valid" },
  ]) {
    assert.throws(() => buildProjectMailboxExportText([row], origin), /邮箱地址格式无效/);
  }
  for (const path of [
    "https://other.example.test/api/v1/otp?token=secret",
    "//other.example.test/api/v1/otp?token=secret",
    "/api/v1/otp?token=first&token=second",
    "/api/v1/otp?api_key=secret",
    "/api/v1/mail/recent?api_key=secret",
  ]) {
    assert.throws(() => buildProjectMailboxExportText([
      { address: "x@icloud.com", otp_url_path: path },
    ], origin), /链接格式无效/);
  }
});

test("download filename uses readable project and account names, stripping filename separators", () => {
  assert.equal(
    buildProjectMailboxFilename("项目 A / 主线", "owner@icloud.com"),
    "项目-A---主线-owner@icloud.com-mailboxes.txt",
  );
  assert.equal(
    buildProjectMailboxFilename("../Other:<Team>", "user\\name@icloud.com"),
    "-Other-Team--user-name@icloud.com-mailboxes.txt",
  );
  assert.equal(buildProjectMailboxFilename("", ""), "project-account-mailboxes.txt");
});

test("download gathers every page for one project and account", async () => {
  const rows = Array.from({ length: 125 }, (_, index) => ({
    alias_id: index + 1,
    address: `user${index + 1}@icloud.com`,
    otp_url_path: `/api/v1/otp?token=key-${index + 1}`,
  }));
  const calls = [];
  const result = await collectProjectMailboxes(({ offset, limit }) => {
    calls.push({ offset, limit });
    return Promise.resolve({ items: rows.slice(offset, offset + limit), total: rows.length });
  }, 50);

  assert.deepEqual(calls, [
    { offset: 0, limit: 50 },
    { offset: 50, limit: 50 },
    { offset: 100, limit: 50 },
  ]);
  assert.deepEqual(result, rows);
  assert.equal(buildProjectMailboxExportText(result, origin).split("\r\n").length, 125);
});

test("empty lists and incomplete pagination do not produce partial files", async () => {
  assert.deepEqual(await collectProjectMailboxes(async () => ({ items: [], total: 0 })), []);
  await assert.rejects(collectProjectMailboxes(async ({ offset }) => ({
    items: offset ? [] : [{ address: "x@icloud.com" }], total: 2,
  })), /读取未完成/);
  await assert.rejects(collectProjectMailboxes(async () => ({
    items: [], total: "100",
  })), /响应格式无效/);
});

test("shared tab requests project/account-scoped mailboxes with the existing admin request helper", async () => {
  const source = await readFile(new URL("../src/views/PoolView.vue", import.meta.url), "utf8");
  assert.match(source, /label="项目共用邮箱" name="shared"/);
  assert.match(source, /sharedAccounts\.value = accountsList\.filter\(\(account\) => account\.enabled && account\.mailboxType === "icloud"\)/);
  assert.match(source, /account_id: accountID,[\s\S]*?limit: sharedPageSize,[\s\S]*?offset: \(sharedPage\.value - 1\) \* sharedPageSize/);
  assert.match(source, /`\/pool\/projects\/\$\{encodeURIComponent\(sharedClientId\.value\)\}\/mailboxes`, "POST", \{\s*account_id: Number\(sharedAccountId\.value\)/);
  assert.match(source, /`\/pool\/projects\/\$\{encodeURIComponent\(sharedClientId\.value\)\}\/mailboxes\/\$\{encodeURIComponent\(row\.alias_id\)\}`, "DELETE"/);
  assert.match(source, /collectProjectMailboxes\(\(\{ limit, offset \}\) => request\(/);
  assert.match(source, /const request = \(path, method = "GET", body, signal\) =>\s*apiRequest\(path, \{ method, body, signal, csrfToken: auth\.state\.csrfToken \}\)/);
  assert.match(source, /buildProjectMailboxExportText\(items, location\.origin\)/);
  assert.match(source, /anchor\.download = buildProjectMailboxFilename\(clientName, accountEmail\)/);
  assert.match(source, /@current-change="loadSharedMailboxes\(\)"/);
  assert.match(source, /new Blob\(\[content\], \{ type: "text\/plain;charset=utf-8" \}\)/);
});

test("claimed leases are labeled but only direct shared associations can be removed", async () => {
  const source = await readFile(new URL("../src/views/PoolView.vue", import.meta.url), "utf8");
  assert.match(source, /shared: "共用关联", claimed: "原有独占领取"/);
  assert.match(source, /v-if="row\.source === 'shared'"[\s\S]*?@click="removeSharedMailbox\(row\)"/);
  assert.match(source, /if \(row\?\.source !== "shared" \|\| !sharedHasSelection\.value/);
  assert.match(source, /if \(e !== "cancel" && e !== "close"\) error\.value = e/);
});
