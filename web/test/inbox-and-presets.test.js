import assert from "node:assert/strict";
import test from "node:test";

import { getActiveAdminSection } from "../src/utils/adminNavigation.js";
import {
  buildInboxShareLink,
  buildSandboxedEmailSrcdoc,
  clearRecentInboxLookups,
  contentStateLabel,
  formatByteSize,
  formatMailParty,
  loadRecentInboxLookups,
  parsePlusTagAddress,
  saveRecentInboxLookup,
} from "../src/utils/inbox.js";
import {
  PROJECT_OTP_PRESETS,
  describeProjectRule,
  normalizeFilterTokens,
} from "../src/utils/projectPresets.js";
import { buildProjectMailboxExportText } from "../src/utils/projectMailboxExport.js";

test("admin navigation maps dashboard and inbox sections while preserving accounts root", () => {
  assert.equal(getActiveAdminSection(""), "accounts");
  assert.equal(getActiveAdminSection("/"), "accounts");
  assert.equal(getActiveAdminSection("/dashboard"), "dashboard");
  assert.equal(getActiveAdminSection("/inbox"), "inbox");
});

test("parsePlusTagAddress splits subaddress tags cleanly", () => {
  assert.deepEqual(parsePlusTagAddress("  Foo+ChatGPT@iCloud.com "), {
    normalized: "foo+chatgpt@icloud.com",
    rootAddress: "foo@icloud.com",
    tag: "chatgpt",
  });
  assert.deepEqual(parsePlusTagAddress("plain@icloud.com"), {
    normalized: "plain@icloud.com",
    rootAddress: "plain@icloud.com",
    tag: "",
  });
});

test("inbox helpers format mail parties, sizes, states, share links, and sandboxed srcdoc", () => {
  assert.equal(
    formatMailParty([
      { name: "OpenAI", email: "noreply@tm.openai.com" },
      { name: "user@icloud.com", email: "user@icloud.com" },
    ]),
    "OpenAI <noreply@tm.openai.com>, user@icloud.com",
  );
  assert.equal(formatByteSize(512), "512 B");
  assert.equal(formatByteSize(2048), "2 KB");
  assert.match(contentStateLabel("evicted"), /淘汰/);

  const srcdoc = buildSandboxedEmailSrcdoc("<p>Safe <b>654321</b></p>", { dark: true });
  assert.match(srcdoc, /<p>Safe <b>654321<\/b><\/p>/);
  assert.doesNotMatch(srcdoc, /<script/i);

  assert.equal(
    buildInboxShareLink("/inbox/view?token=icm_test", "https://mail.example.test:8443"),
    "https://mail.example.test:8443/inbox/view?token=icm_test",
  );
  assert.throws(
    () => buildInboxShareLink("https://evil.example/inbox/view?token=icm_test", "https://mail.example.test"),
    /格式无效/,
  );
});

test("recent inbox lookup storage deduplicates and caps history", () => {
  const map = new Map();
  const memoryStorage = {
    getItem: (k) => map.get(k) ?? null,
    setItem: (k, v) => map.set(k, v),
    removeItem: (k) => map.delete(k),
  };
  saveRecentInboxLookup({ id: 1, address: "box+chatgpt@icloud.com", label: "AI" }, memoryStorage);
  saveRecentInboxLookup({ id: 2, address: "other@icloud.com" }, memoryStorage);
  saveRecentInboxLookup({ id: 1, address: "box@icloud.com", tag: "chatgpt", label: "AI Updated" }, memoryStorage);

  const loaded = loadRecentInboxLookups(memoryStorage);
  assert.equal(loaded.length, 2);
  assert.equal(loaded[0].address, "box@icloud.com");
  assert.equal(loaded[0].tag, "chatgpt");
  assert.equal(loaded[0].label, "AI Updated");

  clearRecentInboxLookups(memoryStorage);
  assert.deepEqual(loadRecentInboxLookups(memoryStorage), []);
});

test("project OTP presets normalize tokens and export project-scoped links when requested", () => {
  assert.ok(PROJECT_OTP_PRESETS.length >= 10);
  assert.equal(
    normalizeFilterTokens(" OpenAI.com, tm.openai.com；openai.com \n "),
    "openai.com,tm.openai.com",
  );
  assert.match(
    describeProjectRule({ sender_filter: "openai.com", subject_filter: "code" }),
    /发件人包含 openai\.com.*主题包含 code/,
  );

  const rows = [
    {
      address: "shared@icloud.com",
      otp_url_path: "/api/v1/otp?token=icm_root",
      project_otp_url_path: "/api/v1/otp?token=icp_project_scoped",
    },
  ];
  assert.equal(
    buildProjectMailboxExportText(rows, "https://mail.example.test"),
    "shared@icloud.com-----https://mail.example.test/api/v1/otp?token=icm_root",
  );
  assert.equal(
    buildProjectMailboxExportText(rows, "https://mail.example.test", { preferProjectToken: true }),
    "shared@icloud.com-----https://mail.example.test/api/v1/otp?token=icp_project_scoped",
  );
});
