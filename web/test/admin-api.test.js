import assert from "node:assert/strict";
import test from "node:test";

import {
  createMailGroup,
  deleteAlias,
  deleteAliases,
  deleteMailGroup,
  deleteAppleSession,
  getAutoCreateLogRun,
  getAccount,
  getAccountPage,
  getAllAccounts,
  getAllAuditLogs,
  getAccounts,
  getAliasPage,
  getAliasDeletionJob,
  getLatestAliasDeletionJob,
  getAllAliases,
  getMailGroups,
  getAllRuntimeLogs,
  getAliases,
  getAuditLogs,
  getRuntimeLogRun,
  getRuntimeLogs,
  loginAppleSession,
  moveAliasToGroup,
  moveAliasesToGroup,
  normalizeAutoCreation,
  normalizeAliasDeletionJob,
  rotateAlias,
  rotateAllAliasCredentials,
  setAliasAutoCreation,
  syncAccount,
  syncAccountAliases,
  startAliasDeletionJob,
  updateMailGroup,
  verifyAppleSession,
} from "../src/api/admin.js";

function jsonResponse(data, status = 200) {
  return new Response(JSON.stringify({ data }), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

test("account list normalizes sync progress and detailed error logs", async () => {
  globalThis.fetch = async () =>
    jsonResponse([
      {
        id: 12,
        email: "owner@icloud.com",
        last_sync_error: "同步失败",
        last_sync_error_log: "fetch IMAP mailbox increment: connection closed",
        sync_progress: {
          active: true,
          source: "automatic",
          stage: "fetching",
          percentage: 0,
          started_at: "2026-08-09T08:00:00Z",
          updated_at: "2026-08-09T08:00:01Z",
        },
      },
      { id: 13, email: "missing@icloud.com" },
      { id: 14, email: "idle@icloud.com", sync_progress: null },
    ]);

  const accounts = await getAccounts();

  assert.deepEqual(accounts[0].syncProgress, {
    active: true,
    source: "automatic",
    stage: "fetching",
    percentage: 0,
    startedAt: "2026-08-09T08:00:00Z",
    updatedAt: "2026-08-09T08:00:01Z",
  });
  assert.equal(
    accounts[0].lastSyncErrorLog,
    "fetch IMAP mailbox increment: connection closed",
  );
  assert.equal(accounts[1].syncProgress, null);
  assert.equal(accounts[1].lastSyncErrorLog, "");
  assert.equal(accounts[2].syncProgress, null);
});

test("account pages send server-side search and normalize pagination metadata", async () => {
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return jsonResponse({
      items: [{ id: 12, email: "owner@icloud.com", alias_count: 7 }],
      pagination: { total: 73, limit: 50, offset: 50, has_more: false },
    });
  };

  const page = await getAccountPage({
    limit: 50,
    offset: 50,
    query: " owner ",
  });
  const url = new URL(request.url, "https://admin.invalid");

  assert.equal(url.pathname, "/admin/api/v1/accounts");
  assert.deepEqual(Object.fromEntries(url.searchParams), {
    limit: "50",
    offset: "50",
    query: "owner",
  });
  assert.equal(request.options.method, "GET");
  assert.deepEqual(
    {
      ids: page.items.map((account) => account.id),
      total: page.total,
      limit: page.limit,
      offset: page.offset,
      hasMore: page.hasMore,
    },
    { ids: [12], total: 73, limit: 50, offset: 50, hasMore: false },
  );
});

test("full account lists follow bounded offset pages and forward abort signals", async () => {
  const requests = [];
  const controller = new AbortController();
  const pages = [
    {
      items: [
        { id: 12, email: "one@icloud.com" },
        { id: 13, email: "two@icloud.com" },
      ],
      pagination: { total: 3, limit: 1000, offset: 0, has_more: true },
    },
    {
      items: [{ id: 14, email: "three@icloud.com" }],
      pagination: { total: 3, limit: 1000, offset: 2, has_more: false },
    },
  ];
  globalThis.fetch = async (url, options) => {
    requests.push({ url: new URL(url, "https://admin.invalid"), options });
    return jsonResponse(pages.shift());
  };

  const accounts = await getAllAccounts({
    query: "owner",
    signal: controller.signal,
  });

  assert.deepEqual(accounts.map((account) => account.id), [12, 13, 14]);
  assert.equal(requests.length, 2);
  for (const { url, options } of requests) {
    assert.equal(url.searchParams.get("limit"), "1000");
    assert.equal(url.searchParams.get("query"), "owner");
    assert.equal(options.signal, controller.signal);
  }
  assert.equal(requests[0].url.searchParams.get("offset"), "0");
  assert.equal(requests[1].url.searchParams.get("offset"), "2");
});

test("account detail accepts camelCase progress and falls back to the sync error", async () => {
  globalThis.fetch = async () =>
    jsonResponse({
      account: {
        id: 12,
        email: "owner@icloud.com",
        lastSyncError: "连接被远端关闭",
        syncProgress: {
          active: true,
          source: "manual",
          stage: "saving",
          percentage: 125,
          startedAt: "2026-08-09T08:00:00Z",
          updatedAt: "2026-08-09T08:00:02Z",
        },
      },
      aliases: [],
    });

  const detail = await getAccount(12);

  assert.deepEqual(detail.account.syncProgress, {
    active: true,
    source: "manual",
    stage: "saving",
    percentage: 100,
    startedAt: "2026-08-09T08:00:00Z",
    updatedAt: "2026-08-09T08:00:02Z",
  });
  assert.equal(detail.account.lastSyncErrorLog, "连接被远端关闭");
});

test("account detail sends bounded alias pagination and normalizes page metadata", async () => {
  let request;
  const controller = new AbortController();
  globalThis.fetch = async (url, options) => {
    request = { url: new URL(url, "https://admin.invalid"), options };
    return jsonResponse({
      account: {
        id: 12,
        email: "owner@icloud.com",
        alias_count: 137,
      },
      aliases: [{ id: 81, address: "page@icloud.com" }],
      pagination: { total: 137, limit: 50, offset: 100, has_more: true },
    });
  };

  const detail = await getAccount(12, {
    limit: 50,
    offset: 100,
    signal: controller.signal,
  });

  assert.equal(request.url.pathname, "/admin/api/v1/accounts/12");
  assert.deepEqual(Object.fromEntries(request.url.searchParams), {
    limit: "50",
    offset: "100",
  });
  assert.equal(request.options.signal, controller.signal);
  assert.equal(detail.account.aliasCount, 137);
  assert.deepEqual(detail.aliases.map((alias) => alias.id), [81]);
  assert.deepEqual(detail.pagination, {
    total: 137,
    limit: 50,
    offset: 100,
    hasMore: true,
  });
});

test("mail sync accepts PascalCase progress and normalizes percentage bounds", async () => {
  const progressCases = [
    {
      Percentage: -20,
      expectedPercentage: 0,
    },
    {
      Percentage: "unknown",
      expectedPercentage: null,
    },
  ];
  const responseQueue = [...progressCases];
  globalThis.fetch = async () => {
    const { expectedPercentage: _expectedPercentage, ...progress } =
      responseQueue.shift();
    return jsonResponse(
      {
        account: {
          ID: 12,
          Email: "owner@icloud.com",
          LastSyncError: "同步失败",
          LastSyncErrorLog: "完整错误日志",
          SyncProgress: {
            Active: true,
            Source: "manual",
            Stage: "connecting",
            StartedAt: "2026-08-09T08:00:00Z",
            UpdatedAt: "2026-08-09T08:00:03Z",
            ...progress,
          },
        },
        aliases: [],
        pagination: { total: 41, limit: 20, offset: 20, has_more: true },
        SyncPending: true,
      },
      202,
    );
  };

  for (const progressCase of progressCases) {
    const detail = await syncAccount(12, "csrf-token");
    assert.equal(
      detail.account.syncProgress.percentage,
      progressCase.expectedPercentage,
    );
    assert.equal(detail.account.lastSyncErrorLog, "完整错误日志");
    assert.equal(detail.syncPending, true);
    assert.deepEqual(detail.pagination, {
      total: 41,
      limit: 20,
      offset: 20,
      hasMore: true,
    });
  }
});

test("account detail includes the normalized Apple session", async () => {
  globalThis.fetch = async () =>
    jsonResponse({
      account: {
        id: 12,
        email: "owner@icloud.com",
        enabled: true,
        alias_count: 1,
      },
      aliases: [{ id: 8, address: "private@icloud.com", enabled: true }],
      apple_session: {
        status: "authenticated",
        apple_id: "owner@icloud.com",
        region: "cn",
        authenticated_at: "2026-08-07T08:00:00Z",
      },
    });

  const detail = await getAccount(12);

  assert.equal(detail.account.id, 12);
  assert.equal(detail.aliases[0].address, "private@icloud.com");
  assert.deepEqual(detail.appleSession, {
    status: "authenticated",
    appleId: "owner@icloud.com",
    region: "cn",
    authenticatedAt: "2026-08-07T08:00:00Z",
    expiresAt: null,
  });
});

test("account detail normalizes automatic alias creation state", async () => {
  globalThis.fetch = async () =>
    jsonResponse({
      account: { id: 12, email: "owner@icloud.com" },
      aliases: [],
      apple_session: null,
      auto_creation: {
        enabled: true,
        status: "scheduled",
        next_run_at: "2026-08-08T09:00:00Z",
        planned_at: "2026-08-08T09:00:00Z",
        planned_times: [
          "2026-08-08T09:00:00Z",
          "2026-08-08T09:15:00Z",
          "2026-08-08T09:35:00Z",
        ],
        last_attempted_at: "2026-08-08T08:00:00Z",
        last_created_at: "2026-08-08T07:00:00Z",
        last_alias_address: "new@icloud.com",
        last_error: "",
        recent_created_count: 2,
        today_created_count: 5,
        today_created_since: "2026-08-08T00:00:00Z",
      },
    });

  const detail = await getAccount(12);

  assert.deepEqual(detail.autoCreation, {
    enabled: true,
    status: "scheduled",
    nextRunAt: "2026-08-08T09:00:00Z",
    plannedAt: "2026-08-08T09:00:00Z",
    plannedTimes: [
      "2026-08-08T09:00:00Z",
      "2026-08-08T09:15:00Z",
      "2026-08-08T09:35:00Z",
    ],
    recentCreatedCount: 2,
    todayCreatedCount: 5,
    recentCreatedSince: null,
    todayCreatedSince: "2026-08-08T00:00:00Z",
    lastAttemptedAt: "2026-08-08T08:00:00Z",
    lastCreatedAt: "2026-08-08T07:00:00Z",
    lastAliasAddress: "new@icloud.com",
    lastError: "",
  });

  assert.deepEqual(
    normalizeAutoCreation({
      Enabled: true,
      Status: "ready",
      NextRunAt: "2026-08-08T10:00:00Z",
      PlannedAt: "2026-08-08T10:00:00Z",
      PlannedTimes: [
        "2026-08-08T10:00:00Z",
        "2026-08-08T10:20:00Z",
      ],
      LastAttemptedAt: "2026-08-08T09:00:00Z",
      LastCreatedAt: "2026-08-08T08:00:00Z",
      LastAliasAddress: "pascal@icloud.com",
      LastError: "temporary failure",
      PendingKeyCount: -2,
      RecentCreatedCount: 89,
      TodayCreatedCount: 89,
    }),
    {
      enabled: true,
      status: "ready",
      nextRunAt: "2026-08-08T10:00:00Z",
      plannedAt: "2026-08-08T10:00:00Z",
      plannedTimes: [
        "2026-08-08T10:00:00Z",
        "2026-08-08T10:20:00Z",
      ],
      recentCreatedCount: 89,
      todayCreatedCount: 89,
      recentCreatedSince: null,
      todayCreatedSince: null,
      lastAttemptedAt: "2026-08-08T09:00:00Z",
      lastCreatedAt: "2026-08-08T08:00:00Z",
      lastAliasAddress: "pascal@icloud.com",
      lastError: "temporary failure",
    },
  );
});

test("automatic alias creation toggle uses an encoded account URL and CSRF", async () => {
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return jsonResponse({
      auto_creation: {
        enabled: true,
        status: "scheduled",
      },
    });
  };

  const result = await setAliasAutoCreation("account/12", true, "csrf-token");

  assert.equal(
    request.url,
    "/admin/api/v1/accounts/account%2F12/aliases/auto-create",
  );
  assert.equal(request.options.method, "PUT");
  assert.equal(request.options.headers.get("X-CSRF-Token"), "csrf-token");
  assert.deepEqual(JSON.parse(request.options.body), { enabled: true });
  assert.deepEqual(result, {
    enabled: true,
    status: "scheduled",
    nextRunAt: null,
    plannedAt: null,
    plannedTimes: [],
    recentCreatedCount: null,
    todayCreatedCount: null,
    recentCreatedSince: null,
    todayCreatedSince: null,
    lastAttemptedAt: null,
    lastCreatedAt: null,
    lastAliasAddress: "",
    lastError: "",
  });
});

test("alias deletion sends an authenticated DELETE without a request body", async () => {
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return new Response(null, { status: 204 });
  };

  const result = await deleteAlias("alias/91", "csrf-token");

  assert.equal(result, null);
  assert.equal(request.url, "/admin/api/v1/aliases/alias%2F91");
  assert.equal(request.options.method, "DELETE");
  assert.equal(request.options.headers.get("X-CSRF-Token"), "csrf-token");
  assert.equal(request.options.body, undefined);
});

test("batch alias deletion sends IDs and normalizes per-item Apple results", async () => {
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return jsonResponse({
      requested: 2,
      deleted: 1,
      failed: 1,
      results: [
        { id: 91, address: "removed@icloud.com", deleted: true },
        {
          id: 92,
          address: "retained@icloud.com",
          deleted: false,
          code: "APPLE_RATE_LIMITED",
          message: "Apple 请求过于频繁；本地记录已保留，可稍后重试",
          local_retained: true,
        },
      ],
    });
  };

  const result = await deleteAliases([91, 92], "csrf-token");

  assert.equal(request.url, "/admin/api/v1/aliases/batch");
  assert.equal(request.options.method, "DELETE");
  assert.equal(request.options.headers.get("X-CSRF-Token"), "csrf-token");
  assert.deepEqual(JSON.parse(request.options.body), { alias_ids: [91, 92] });
  assert.deepEqual(result, {
    requested: 2,
    deleted: 1,
    failed: 1,
    results: [
      {
        id: 91,
        address: "removed@icloud.com",
        deleted: true,
        code: "",
        message: "",
        localRetained: false,
      },
      {
        id: 92,
        address: "retained@icloud.com",
        deleted: false,
        code: "APPLE_RATE_LIMITED",
        message: "Apple 请求过于频繁；本地记录已保留，可稍后重试",
        localRetained: true,
      },
    ],
  });
});

test("v2 alias rotation uses the complete bundle endpoint", async () => {
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return jsonResponse({
      id: 91,
      address: "alias@example.com",
      api_key: "api-key",
      imap_password: "imap-password",
      client_id: "client-id",
      refresh_token: "refresh-token",
      otp_url_path: "/api/v1/otp?token=derived-token",
      credential_version: 2,
      enabled: true,
    });
  };

  const result = await rotateAlias("alias/91", "csrf-token", "v2");

  assert.equal(
    request.url,
    "/admin/api/v1/aliases/alias%2F91/rotate-credentials",
  );
  assert.equal(request.options.method, "POST");
  assert.equal(request.options.headers.get("X-CSRF-Token"), "csrf-token");
  assert.equal(request.options.body, undefined);
  assert.deepEqual(
    {
      apiKey: result.alias.apiKey,
      imapPassword: result.alias.imapPassword,
      clientId: result.alias.clientId,
      refreshToken: result.alias.refreshToken,
      otpUrlPath: result.alias.otpUrlPath,
      credentialVersion: result.alias.credentialVersion,
    },
    {
      apiKey: "api-key",
      imapPassword: "imap-password",
      clientId: "client-id",
      refreshToken: "refresh-token",
      otpUrlPath: "/api/v1/otp?token=derived-token",
      credentialVersion: 2,
    },
  );
});

test("legacy alias rotation uses the API-key-only compatibility endpoint", async () => {
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return jsonResponse({
      alias: {
        id: 91,
        address: "legacy@example.com",
        api_key_prefix: "new-key-",
        credential_mode: "legacy",
        direct_link_path: "/api/v1/mail/recent?api_key=new-token",
        enabled: true,
      },
      api_key: "new-key-secret",
    });
  };

  const result = await rotateAlias("alias/91", "csrf-token", "legacy");

  assert.equal(request.url, "/admin/api/v1/aliases/alias%2F91/rotate-key");
  assert.equal(request.options.method, "POST");
  assert.equal(request.options.headers.get("X-CSRF-Token"), "csrf-token");
  assert.equal(request.options.body, undefined);
  assert.equal(result.alias.credentialMode, "legacy");
  assert.equal(result.alias.directLinkPath, "/api/v1/mail/recent?api_key=new-token");
  assert.equal(result.apiKey, "new-key-secret");
});

test("all-alias credential rotation requires explicit confirmation and normalizes its summary", async () => {
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return jsonResponse({
      total: 12,
      rotated: 12,
      migrated_legacy: 4,
      rotated_v2: 8,
      rotated_pending: 2,
      reauthentication_required: true,
    });
  };

  const result = await rotateAllAliasCredentials(
    "current-password",
    "csrf-token",
  );

  assert.equal(request.url, "/admin/api/v1/aliases/rotate-all-credentials");
  assert.equal(request.options.method, "POST");
  assert.equal(request.options.headers.get("X-CSRF-Token"), "csrf-token");
  assert.deepEqual(JSON.parse(request.options.body), {
    confirmation: "ROTATE_ALL",
    current_password: "current-password",
  });
  assert.deepEqual(result, {
    total: 12,
    rotated: 12,
    migratedLegacy: 4,
    rotatedV2: 8,
    rotatedPending: 2,
    reauthenticationRequired: true,
  });
});

test("all-alias credential rotation rejects incomplete or inconsistent summaries", async () => {
  const invalidSummaries = [
    {
      total: 12,
      rotated: 12,
      migrated_legacy: 4,
      rotated_v2: 8,
      reauthentication_required: true,
    },
    {
      total: 12,
      rotated: 12,
      migrated_legacy: 4,
      rotated_v2: 7,
      rotated_pending: 2,
      reauthentication_required: true,
    },
    {
      total: 12,
      rotated: 12,
      migrated_legacy: 4,
      rotated_v2: 8,
      rotated_pending: 13,
      reauthentication_required: true,
    },
    {
      total: 12,
      rotated: 12,
      migrated_legacy: 4,
      rotated_v2: 8,
      rotated_pending: 2,
      reauthentication_required: false,
    },
  ];

  for (const summary of invalidSummaries) {
    globalThis.fetch = async () => jsonResponse(summary);
    await assert.rejects(
      rotateAllAliasCredentials("current-password", "csrf-token"),
      (error) => {
        assert.equal(error.code, "ROTATION_RESULT_INVALID");
        assert.match(error.message, /轮换已提交/);
        return true;
      },
    );
  }
});

test("alias directory forwards the optional primary-account filter", async () => {
  const requests = [];
  globalThis.fetch = async (url, options) => {
    requests.push({ url, options });
    return jsonResponse([
      {
        id: 8,
        account_id: 12,
        account_email: "owner@icloud.com",
        address: "private@icloud.com",
        enabled: true,
        last_sync_error: "连接失败",
        last_sync_error_log: "fetch IMAP mailbox increment: connection closed",
      },
      {
        id: 9,
        account_id: 12,
        account_email: "owner@icloud.com",
        address: "fallback@icloud.com",
        enabled: true,
        last_sync_error: "仅有错误摘要",
      },
    ]);
  };

  const filtered = await getAliases(12);
  const all = await getAliases();

  const filteredURL = new URL(requests[0].url, "https://admin.invalid");
  const allURL = new URL(requests[1].url, "https://admin.invalid");
  assert.equal(filteredURL.pathname, "/admin/api/v1/aliases");
  assert.equal(filteredURL.searchParams.get("account_id"), "12");
  assert.equal(filteredURL.searchParams.get("limit"), "1000");
  assert.equal(filteredURL.searchParams.get("offset"), "0");
  assert.equal(allURL.pathname, "/admin/api/v1/aliases");
  assert.equal(allURL.searchParams.has("account_id"), false);
  assert.equal(allURL.searchParams.get("limit"), "1000");
  assert.equal(allURL.searchParams.get("offset"), "0");
  assert.equal(filtered[0].accountId, 12);
  assert.equal(
    filtered[0].lastSyncErrorLog,
    "fetch IMAP mailbox increment: connection closed",
  );
  assert.equal(filtered[1].lastSyncErrorLog, "仅有错误摘要");
  assert.equal(all[0].address, "private@icloud.com");
});

for (const [option, parameter] of [
  ["withoutLatestMail", "without_latest_mail"],
  ["withLatestMail", "with_latest_mail"],
]) {
test(`alias pages apply ${parameter}, search and account filters before pagination`, async () => {
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return jsonResponse({
      items: [
        {
          id: 8,
          account_id: 12,
          address: "private@icloud.com",
          enabled: true,
        },
      ],
      pagination: { total: 81, limit: 50, offset: 50, has_more: false },
    });
  };

  const page = await getAliasPage(12, {
    limit: 50,
    offset: 50,
    query: "  private+box  ",
    groupId: 7,
    [option]: true,
  });
  const url = new URL(request.url, "https://admin.invalid");

  assert.deepEqual(Object.fromEntries(url.searchParams), {
    limit: "50",
    offset: "50",
    account_id: "12",
    group_id: "7",
    query: "private+box",
    [parameter]: "true",
  });
  assert.equal(page.items[0].accountId, 12);
  assert.deepEqual(
    { total: page.total, limit: page.limit, offset: page.offset, hasMore: page.hasMore },
    { total: 81, limit: 50, offset: 50, hasMore: false },
  );
});
}

test("async alias deletion sends the operation ID once and maps the 202 snapshot exactly", async () => {
  const operationId = "63c21a27-6ef4-425f-a11c-edda65c29267";
  const controller = new AbortController();
  const requests = [];
  const raw = {
    job_id: operationId, status: "running", requested: 9, processed: 2,
    deleted: 1, failed: 1, request_id: "request-42",
    created_at: "2026-09-08T01:00:00Z", updated_at: "2026-09-08T01:00:01Z",
    results: [
      { id: 91, address: "deleted@icloud.com", deleted: true },
      { id: 92, address: "retained@icloud.com", deleted: false,
        code: "APPLE_RATE_LIMITED", message: "稍后核对", local_retained: true },
    ],
  };
  globalThis.fetch = async (url, options) => {
    requests.push({ url, options });
    return jsonResponse(raw, 202);
  };

  const result = await startAliasDeletionJob([91, 92], operationId, "csrf-token", {
    signal: controller.signal,
  });
  assert.equal(requests.length, 1);
  const { url, options } = requests[0];
  assert.equal(url, "/admin/api/v1/aliases/batch?async=1");
  assert.equal(options.method, "DELETE");
  assert.equal(options.headers.get("X-CSRF-Token"), "csrf-token");
  assert.equal(options.signal, controller.signal);
  assert.deepEqual(JSON.parse(options.body), { alias_ids: [91, 92], operation_id: operationId });
  assert.deepEqual(result, {
    jobId: operationId, status: "running", requested: 9, processed: 2,
    deleted: 1, failed: 1, requestId: "request-42",
    waits: [], deferred: 0,
    createdAt: raw.created_at, updatedAt: raw.updated_at,
    results: [
      { id: 91, address: "deleted@icloud.com", deleted: true, code: "", message: "", localRetained: false },
      { id: 92, address: "retained@icloud.com", deleted: false,
        code: "APPLE_RATE_LIMITED", message: "稍后核对", localRetained: true },
    ],
  });
});

test("job reads encode IDs, forward cancellation, and preserve latest null", async () => {
  const requests = [];
  const controller = new AbortController();
  globalThis.fetch = async (url, options) => {
    requests.push({ url, options });
    return jsonResponse(url.endsWith("/latest") ? null : {
      job_id: "job/id?value", status: "queued", requested: 2, processed: 0,
      deleted: 0, failed: 0, results: [],
    });
  };
  assert.equal((await getAliasDeletionJob("job/id?value", { signal: controller.signal })).status, "queued");
  assert.equal(await getLatestAliasDeletionJob({ signal: controller.signal }), null);
  assert.deepEqual(requests.map(({ url }) => url), [
    "/admin/api/v1/aliases/batch/jobs/job%2Fid%3Fvalue",
    "/admin/api/v1/aliases/batch/jobs/latest",
  ]);
  for (const { options } of requests) {
    assert.equal(options.method, "GET");
    assert.equal(options.body, undefined);
    assert.equal(options.signal, controller.signal);
  }
});

test("all job endpoints map wait DTOs and deferred counts without changing the mutation", async () => {
  const rawWaits = ["validate", "list", "deactivate", "delete"].map((operation, index) => ({
    account_id: index === 3 ? 0 : 12, alias_id: index === 0 ? 0 : 91 + index,
    operation, retry_at: "2026-09-08T09:02:00.123456789+08:00",
    attempt: index % 3 + 1, max_attempts: 3, http_status: 429, service_code: "-21669",
    raw_body: "upstream-secret", rawBody: "upstream-secret", message: "upstream-secret",
  }));
  const raw = {
    job_id: "waiting-job", status: "running", requested: 416, processed: 2,
    deleted: 0, failed: 2, deferred: 1, waits: rawWaits,
    results: [{ id: 90, deleted: false, code: "APPLE_BATCH_DEFERRED", local_retained: true }],
  };
  const requests = [];
  globalThis.fetch = async (url, options) => {
    requests.push({ url, options });
    return jsonResponse({ job: raw }, options.method === "DELETE" ? 202 : 200);
  };

  const submitted = await startAliasDeletionJob([91, 92], "waiting-job", "csrf");
  const polled = await getAliasDeletionJob("waiting-job");
  const latest = await getLatestAliasDeletionJob();
  assert.deepEqual(submitted, polled);
  assert.deepEqual(latest, polled);
  assert.deepEqual(polled.waits, rawWaits.map((wait) => ({
    accountId: wait.account_id, aliasId: wait.alias_id, operation: wait.operation,
    retryAt: wait.retry_at, attempt: wait.attempt, maxAttempts: 3,
    httpStatus: 429, serviceCode: "-21669",
  })));
  assert.equal(polled.status, "running");
  assert.deepEqual([polled.requested, polled.processed, polled.deleted, polled.failed, polled.deferred], [416, 2, 0, 2, 1]);
  assert.equal(polled.results[0].code, "APPLE_BATCH_DEFERRED");
  assert.equal(polled.results[0].localRetained, true);
  assert.doesNotMatch(JSON.stringify(polled), /upstream-secret|raw_body|rawBody/);
  assert.deepEqual(requests.map(({ options }) => options.method), ["DELETE", "GET", "GET"]);
  assert.deepEqual(JSON.parse(requests[0].options.body), { alias_ids: [91, 92], operation_id: "waiting-job" });
  assert.equal(requests[1].options.body, undefined);
  assert.equal(requests[2].options.body, undefined);
});

test("optional malformed wait metadata is isolated from the original job and valid waits", () => {
  const raw = {
    job_id: "job", status: "running", requested: 416, processed: 0,
    deleted: 0, failed: 0, results: [],
  };
  const validWait = {
    account_id: 12, alias_id: 91, operation: "delete",
    retry_at: "2026-09-08T01:01:00Z", attempt: 1, max_attempts: 3,
  };
  const expected = normalizeAliasDeletionJob(raw);
  assert.deepEqual(expected.waits, []);
  assert.equal(expected.deferred, 0);
  for (const waits of [undefined, null, false, 42, "upstream-secret", { raw_body: "secret" }]) {
    assert.deepEqual(normalizeAliasDeletionJob({ ...raw, waits }), expected);
  }
  const invalidWaits = [null, [], false, "secret", {}, ...[
    { account_id: "12" }, { account_id: { secret: "private" } }, { alias_id: -1 },
    { alias_id: Number.MAX_SAFE_INTEGER + 1 }, { account_id: 0, alias_id: 0 },
    { operation: "__proto__" }, { operation: "constructor" }, { operation: "reserve" },
    { operation: "<img src=x onerror=alert(1)>" },
    { retry_at: "<script>secret</script>" }, { retry_at: "2026-13-08T01:01:00Z" },
    { retry_at: "2026-09-08" }, { retry_at: "2026-09-08T01:01:00" },
    { retry_at: { secret: "private" } }, { retry_at: ["2026-09-08T01:01:00Z"] },
    { attempt: 0 }, { attempt: 4 }, { attempt: 1.5 }, { attempt: "1" },
    { max_attempts: 4 }, { max_attempts: "3" },
  ].map((patch) => ({ ...validWait, ...patch }))];
  const result = normalizeAliasDeletionJob({ ...raw, waits: [...invalidWaits, validWait] });
  assert.deepEqual(result, { ...expected, waits: [{
    accountId: 12, aliasId: 91, operation: "delete", retryAt: validWait.retry_at,
    attempt: 1, maxAttempts: 3,
  }] });
  for (const optional of [
    { http_status: "429", service_code: { raw_body: "secret" } },
    { http_status: 600, service_code: "<script>secret</script>" },
    { http_status: 99, service_code: "x".repeat(65) },
    { http_status: 429.5, service_code: 429 },
  ]) {
    assert.deepEqual(normalizeAliasDeletionJob({ ...raw, waits: [{ ...validWait, ...optional }] }), result);
  }
  for (const deferred of [undefined, null, -1, 0.5, "1", true, {}, Number.MAX_SAFE_INTEGER + 1, 1]) {
    assert.deepEqual(normalizeAliasDeletionJob({ ...raw, deferred }), expected);
  }
});

test("wait metadata accepts normalized and Pascal-case fields and clears on older snapshots", () => {
  const wait = {
    accountId: 12, aliasId: 91, operation: "list", retryAt: "2026-09-08T01:01:00Z",
    attempt: 3, maxAttempts: 3, httpStatus: 503, serviceCode: "RATE_LIMITED",
  };
  assert.deepEqual(normalizeAliasDeletionJob({ waits: [wait] }).waits, [wait]);
  assert.deepEqual(normalizeAliasDeletionJob({ Waits: [{
    AccountID: 12, AliasID: 91, Operation: "list", RetryAt: wait.retryAt,
    Attempt: 3, MaxAttempts: 3, HTTPStatus: 503, ServiceCode: "RATE_LIMITED",
  }] }).waits, [wait]);
  const raw = { job_id: "job", status: "running", requested: 416, processed: 0, deleted: 0, failed: 0 };
  assert.equal(normalizeAliasDeletionJob({ ...raw, waits: [wait] }).waits.length, 1);
  assert.deepEqual(normalizeAliasDeletionJob(raw).waits, []);
  assert.equal(normalizeAliasDeletionJob({ Failed: 4, Deferred: 3 }).deferred, 3);
  assert.equal(normalizeAliasDeletionJob({ failed: 4, deferred: 5 }).deferred, 0);
});

test("job normalization never derives counters from results or invents retention evidence", () => {
  const result = normalizeAliasDeletionJob({
    job_id: "job", status: "interrupted", requested: 8, processed: 1,
    deleted: 0, failed: 6,
    results: [{ id: 91, deleted: false, code: "BATCH_DELETE_INTERRUPTED" }],
  });
  assert.equal(result.processed, 1);
  assert.equal(result.failed, 6);
  assert.equal(result.deleted, 0);
  assert.equal(result.results[0].localRetained, false);
  const malformed = normalizeAliasDeletionJob({ job_id: "job", results: [{ deleted: "false", local_retained: "false" }] });
  assert.equal(malformed.status, "");
  assert.deepEqual([malformed.requested, malformed.processed, malformed.deleted, malformed.failed], [null, null, null, null]);
  assert.equal(malformed.results[0].deleted, false);
  assert.equal(malformed.results[0].localRetained, false);
  for (const value of [null, -1, 0.5, "2"]) {
    assert.equal(normalizeAliasDeletionJob({ processed: value }).processed, null);
  }
  assert.equal(normalizeAliasDeletionJob(null), null);
  for (const value of [[], "invalid", false]) {
    assert.throws(() => normalizeAliasDeletionJob(value), { code: "INVALID_RESPONSE" });
  }
});

test("async deletion preserves admission errors and request IDs without retrying DELETE", async () => {
  for (const [status, code] of [
    [409, "BATCH_DELETE_IN_PROGRESS"], [409, "IDEMPOTENCY_CONFLICT"],
    [429, "BATCH_DELETE_BUSY"], [503, "BATCH_DELETE_UNAVAILABLE"],
  ]) {
    let calls = 0;
    globalThis.fetch = async () => {
      calls += 1;
      return new Response(JSON.stringify({ error: { code, message: "提交未接收", request_id: "error-request" } }), {
        status, headers: { "Content-Type": "application/json" },
      });
    };
    await assert.rejects(startAliasDeletionJob([91], "operation-id", "csrf"), {
      status, code, requestId: "error-request",
    });
    assert.equal(calls, 1);
  }
});

test("an owner-isolated 404 is propagated rather than replaced by latest", async () => {
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    return new Response(JSON.stringify({ error: { code: "NOT_FOUND" } }), {
      status: 404, headers: { "Content-Type": "application/json" },
    });
  };
  await assert.rejects(getAliasDeletionJob("other-owner-job"), { status: 404, code: "NOT_FOUND" });
  assert.equal(calls, 1);
});

test("alias pages omit disabled latest-mail filters", async () => {
  const requests = [];
  globalThis.fetch = async (url) => {
    requests.push(new URL(url, "https://admin.invalid"));
    return jsonResponse({ items: [], pagination: { total: 0, limit: 20, offset: 0 } });
  };

  await getAliasPage();
  await getAliasPage("", { withoutLatestMail: false, withLatestMail: false });

  assert.equal(requests.length, 2);
  for (const url of requests) {
    assert.equal(url.searchParams.has("without_latest_mail"), false);
    assert.equal(url.searchParams.has("with_latest_mail"), false);
  }
});

test("alias pages preserve enabled=false", async () => {
  let request;
  globalThis.fetch = async (url) => {
    request = new URL(url, "https://admin.invalid");
    return jsonResponse({ items: [], pagination: { total: 0, limit: 20, offset: 0 } });
  };
  await getAliasPage(12, { enabled: false });
  assert.equal(request.searchParams.get("enabled"), "false");
});

test("all alias pages retain account, status and query filters across offsets", async () => {
  const requests = [];
  globalThis.fetch = async (url) => {
    const query = new URL(url, "https://admin.invalid").searchParams;
    requests.push(query);
    const offset = Number(query.get("offset"));
    return jsonResponse({
      items: [{ id: offset + 1, account_id: 12, enabled: false }],
      pagination: { total: 3, limit: 1, offset, has_more: offset < 2 },
    });
  };
  const aliases = await getAllAliases(12, { enabled: false, query: "filter" });
  assert.deepEqual(aliases.map((alias) => alias.id), [1, 2, 3]);
  assert.deepEqual(requests.map((query) => query.get("offset")), ["0", "1", "2"]);
  for (const query of requests) {
    assert.equal(query.get("account_id"), "12");
    assert.equal(query.get("enabled"), "false");
    assert.equal(query.get("query"), "filter");
  }
});

test("mail groups normalize counts and send authenticated mutations", async () => {
  const requests = [];
  const responses = [
    { items: [{ id: 3, name: "工作", alias_count: 2 }] },
    { id: 4, name: "购物", alias_count: 0 },
    { id: 4, name: "订单", alias_count: 0 },
    null,
  ];
  globalThis.fetch = async (url, options) => {
    requests.push({ url: new URL(url, "https://admin.invalid"), options });
    const value = responses.shift();
    if (value === null) return new Response(null, { status: 204 });
    return jsonResponse(value);
  };

  const groups = await getMailGroups();
  const created = await createMailGroup("购物", "csrf-token");
  const updated = await updateMailGroup(4, "订单", "csrf-token");
  await deleteMailGroup(4, "csrf-token");

  assert.deepEqual(groups.map((group) => [group.id, group.name, group.aliasCount]), [
    [3, "工作", 2],
  ]);
  assert.equal(created.name, "购物");
  assert.equal(updated.name, "订单");
  assert.deepEqual(
    requests.map((request) => [request.url.pathname, request.options.method]),
    [
      ["/admin/api/v1/groups", "GET"],
      ["/admin/api/v1/groups", "POST"],
      ["/admin/api/v1/groups/4", "PATCH"],
      ["/admin/api/v1/groups/4", "DELETE"],
    ],
  );
  assert.equal(requests[1].options.headers.get("X-CSRF-Token"), "csrf-token");
  assert.deepEqual(JSON.parse(requests[2].options.body), { name: "订单" });
});

test("alias group filters and moves preserve explicit ungrouping", async () => {
  const requests = [];
  globalThis.fetch = async (url, options) => {
    requests.push({ url: new URL(url, "https://admin.invalid"), options });
    if (options.method === "PATCH" && /\/aliases\/9\/group$/.test(url)) {
      return jsonResponse({
        id: 9,
        address: "private@icloud.com",
        group_id: 7,
        group_name: "注册",
      });
    }
    if (options.method === "PATCH") return new Response(null, { status: 204 });
    return jsonResponse({ items: [], pagination: { total: 0, limit: 20, offset: 0 } });
  };

  await getAliasPage("", { groupId: "none" });
  const moved = await moveAliasToGroup(9, 7, "csrf-token");
  await moveAliasesToGroup([9, 10], null, "csrf-token");

  assert.equal(requests[0].url.searchParams.get("group_id"), "none");
  assert.equal(requests[0].url.searchParams.has("without_latest_mail"), false);
  assert.equal(moved.groupId, 7);
  assert.equal(moved.groupName, "注册");
  assert.deepEqual(JSON.parse(requests[1].options.body), { group_id: 7 });
  assert.deepEqual(JSON.parse(requests[2].options.body), {
    alias_ids: [9, 10],
    group_id: null,
  });
});

for (const [option, parameter] of [
  ["withoutLatestMail", "without_latest_mail"],
  ["withLatestMail", "with_latest_mail"],
]) {
test(`full alias export preserves search and ${parameter} across every page`, async () => {
  const requests = [];
  const pages = [
    {
      items: [{ id: 2, account_id: 12, address: "second@icloud.com" }],
      pagination: { total: 2, limit: 200, offset: 0, has_more: true },
    },
    {
      items: [{ id: 1, account_id: 12, address: "first@icloud.com" }],
      pagination: { total: 2, limit: 200, offset: 1, has_more: false },
    },
  ];
  globalThis.fetch = async (url) => {
    requests.push(new URL(url, "https://admin.invalid"));
    return jsonResponse(pages.shift());
  };

  const aliases = await getAllAliases(12, {
    query: "receipt",
    groupId: "none",
    [option]: true,
  });

  assert.deepEqual(aliases.map((alias) => alias.id), [2, 1]);
  assert.equal(requests.length, 2);
  assert.equal(requests[0].searchParams.get("offset"), "0");
  assert.equal(requests[1].searchParams.get("offset"), "1");
  for (const request of requests) {
    assert.equal(request.searchParams.get("limit"), "1000");
    assert.equal(request.searchParams.get("account_id"), "12");
    assert.equal(request.searchParams.get("group_id"), "none");
    assert.equal(request.searchParams.get("query"), "receipt");
    assert.equal(request.searchParams.get(parameter), "true");
    assert.equal(
      request.searchParams.has(option === "withLatestMail" ? "without_latest_mail" : "with_latest_mail"),
      false,
    );
  }
});
}

test("audit pages preserve total, limit, offset, and has-more metadata", async () => {
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return jsonResponse({
      items: [{ id: 31, action: "update", created_at: "2026-08-09T08:00:00Z" }],
      pagination: { total: 131, limit: 50, offset: 100, has_more: true },
    });
  };

  const page = await getAuditLogs({ limit: 50, offset: 100 });
  const url = new URL(request.url, "https://admin.invalid");

  assert.deepEqual(Object.fromEntries(url.searchParams), {
    limit: "50",
    offset: "100",
  });
  assert.equal(page.items[0].createdAt, "2026-08-09T08:00:00Z");
  assert.deepEqual(
    { total: page.total, limit: page.limit, offset: page.offset, hasMore: page.hasMore },
    { total: 131, limit: 50, offset: 100, hasMore: true },
  );
});

test("full audit and runtime log lists use 1000-row batches", async () => {
  const controller = new AbortController();
  const requests = [];
  const responses = [
    jsonResponse({
      items: [{ id: 31, action: "update" }],
      pagination: { total: 1, limit: 1000, offset: 0, has_more: false },
    }),
    jsonResponse({
      items: [{ id: 41, message: "first" }],
      pagination: { total: 1, limit: 1000, offset: 0, has_more: false },
    }),
  ];
  globalThis.fetch = async (url, options) => {
    requests.push({ url: new URL(url, "https://admin.invalid"), options });
    return responses.shift();
  };

  const audit = await getAllAuditLogs({ signal: controller.signal });
  const runtime = await getAllRuntimeLogs({
    level: "error",
    accountId: 12,
    signal: controller.signal,
  });

  assert.equal(audit[0].id, 31);
  assert.equal(runtime[0].id, 41);
  assert.equal(requests.length, 2);
  assert.equal(requests[0].url.searchParams.get("limit"), "1000");
  assert.equal(requests[1].url.searchParams.get("limit"), "1000");
  assert.equal(requests[1].url.searchParams.get("level"), "error");
  assert.equal(requests[1].url.searchParams.get("account_id"), "12");
  assert.equal(requests[0].options.signal, controller.signal);
  assert.equal(requests[1].options.signal, controller.signal);
});

test("pending mail sync accepts HTTP 202 and exposes continuation state", async () => {
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return jsonResponse(
      {
        account: {
          id: 12,
          email: "owner@icloud.com",
          enabled: true,
          last_sync_status: "pending",
        },
        aliases: [],
        apple_session: null,
        sync_pending: true,
      },
      202,
    );
  };

  const detail = await syncAccount(12, "csrf-token");

  assert.equal(request.url, "/admin/api/v1/accounts/12/sync");
  assert.equal(request.options.method, "POST");
  assert.equal(request.options.headers.get("X-CSRF-Token"), "csrf-token");
  assert.equal(detail.account.lastSyncStatus, "pending");
  assert.equal(detail.syncPending, true);
});

test("Apple session endpoints send credentials and verification only in request bodies", async () => {
  const requests = [];
  const responses = [
    jsonResponse({
      status: "verification_required",
      challenge_id: "challenge-123",
      apple_session: {
        status: "verification_required",
        apple_id: "owner@icloud.com",
        region: "global",
      },
    }),
    jsonResponse({
      status: "authenticated",
      apple_session: {
        status: "authenticated",
        apple_id: "owner@icloud.com",
        region: "global",
      },
    }),
    new Response(null, { status: 204 }),
  ];
  globalThis.fetch = async (url, options) => {
    requests.push({ url, options });
    return responses.shift();
  };

  const loginResult = await loginAppleSession(
    "account/12",
    {
      apple_id: "owner@icloud.com",
      password: "apple-password",
      region: "global",
    },
    "csrf-token",
  );
  const verifyResult = await verifyAppleSession(
    "account/12",
    { challenge_id: loginResult.challengeId, code: "123456" },
    "csrf-token",
  );
  await deleteAppleSession("account/12", "csrf-token");

  assert.equal(loginResult.status, "verification_required");
  assert.equal(verifyResult.status, "authenticated");
  assert.deepEqual(
    requests.map(({ url, options }) => ({
      url,
      method: options.method,
      csrf: options.headers.get("X-CSRF-Token"),
      body: options.body ? JSON.parse(options.body) : null,
    })),
    [
      {
        url: "/admin/api/v1/accounts/account%2F12/apple-auth",
        method: "POST",
        csrf: "csrf-token",
        body: {
          apple_id: "owner@icloud.com",
          password: "apple-password",
          region: "global",
        },
      },
      {
        url: "/admin/api/v1/accounts/account%2F12/apple-auth/verify",
        method: "POST",
        csrf: "csrf-token",
        body: { challenge_id: "challenge-123", code: "123456" },
      },
      {
        url: "/admin/api/v1/accounts/account%2F12/apple-auth",
        method: "DELETE",
        csrf: "csrf-token",
        body: null,
      },
    ],
  );
});

test("alias directory sync normalizes its summary and persistent credential bundles", async () => {
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return jsonResponse({
      account: {
        id: 12,
        email: "owner@icloud.com",
        enabled: true,
        alias_count: 2,
      },
      aliases: [
        { id: 8, address: "existing@icloud.com", enabled: true },
        { id: 9, address: "new@icloud.com", enabled: true },
      ],
      apple_session: {
        status: "authenticated",
        apple_id: "owner@icloud.com",
        region: "global",
      },
      summary: {
        total: 4,
        created_count: 1,
        existing_count: 1,
        inactive_count: 1,
        imported_disabled_count: 1,
        conflict_count: 1,
      },
      detail_stale: true,
      created: [
        {
          alias: {
            id: 9,
            address: "new@icloud.com",
            api_key: "api-key",
            imap_password: "imap-password",
            client_id: "client-id",
            refresh_token: "refresh-token",
            otp_url_path: "/api/v1/otp?token=derived",
            credential_version: 1,
            enabled: true,
          },
        },
      ],
    });
  };

  const result = await syncAccountAliases(12, "csrf-token");

  assert.equal(request.url, "/admin/api/v1/accounts/12/aliases/sync");
  assert.equal(request.options.method, "POST");
  assert.equal(request.options.headers.get("X-CSRF-Token"), "csrf-token");
  assert.deepEqual(result.summary, {
    total: 4,
    createdCount: 1,
    existingCount: 1,
    inactiveCount: 1,
    importedDisabledCount: 1,
    conflictCount: 1,
  });
  assert.equal(result.created[0].apiKey, "api-key");
  assert.equal(result.created[0].otpUrlPath, "/api/v1/otp?token=derived");
  assert.equal(result.detailStale, true);
  assert.deepEqual(
    {
      address: result.created[0].alias.address,
      apiKey: result.created[0].alias.apiKey,
      imapPassword: result.created[0].alias.imapPassword,
      clientId: result.created[0].alias.clientId,
      refreshToken: result.created[0].alias.refreshToken,
      otpUrlPath: result.created[0].alias.otpUrlPath,
      credentialVersion: result.created[0].alias.credentialVersion,
    },
    {
      address: "new@icloud.com",
      apiKey: "api-key",
      imapPassword: "imap-password",
      clientId: "client-id",
      refreshToken: "refresh-token",
      otpUrlPath: "/api/v1/otp?token=derived",
      credentialVersion: 1,
    },
  );
});

test("runtime log pages use offset filters and normalize pagination metadata", async () => {
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return jsonResponse({
      items: [
        {
          id: 81,
          created_at: "2026-08-09T08:00:00Z",
          level: "ERROR",
          message: "主号同步失败",
          source: "syncer.manager",
          account_id: 12,
          request_id: "req-log-1",
          auto_create_run_id: "auto-run-1",
          attributes: {
            auto_create_stage: "failed",
            auto_create_percent: "100",
            auto_create_event: "run_failed",
            error: "connection closed",
            error_code: "APPLE_RATE_LIMITED",
            error_class: "apple_upstream",
            cause_category: "apple_upstream",
            error_context: "Apple 请求被限流",
            failed_stage: "reserving",
            failed_operation: "reserve_alias",
            http_status: "429",
            retryable: "true",
            elapsed_ms: "3821",
            schedule_action: "continue",
          },
        },
      ],
      pagination: { total: 481, limit: 50, offset: 100, has_more: true },
    });
  };

  const result = await getRuntimeLogs({
    level: "error",
    query: "同步失败",
    accountId: 12,
    autoCreateRunId: "auto-run-1",
    limit: 50,
    offset: 100,
  });
  const url = new URL(request.url, "https://admin.invalid");

  assert.equal(url.pathname, "/admin/api/v1/logs");
  assert.deepEqual(Object.fromEntries(url.searchParams), {
    level: "error",
    query: "同步失败",
    account_id: "12",
    auto_create_run_id: "auto-run-1",
    limit: "50",
    offset: "100",
  });
  assert.equal(request.options.method, "GET");
  assert.equal(result.items[0].time, "2026-08-09T08:00:00Z");
  assert.equal(result.items[0].level, "error");
  assert.equal(result.items[0].accountId, 12);
  assert.equal(result.items[0].requestId, "req-log-1");
  assert.equal(result.items[0].autoCreateRunId, "auto-run-1");
  assert.equal(result.items[0].autoCreateStage, "failed");
  assert.equal(result.items[0].autoCreatePercent, 100);
  assert.equal(result.items[0].autoCreateEvent, "run_failed");
  assert.equal(result.items[0].errorCode, "APPLE_RATE_LIMITED");
  assert.equal(result.items[0].errorClass, "apple_upstream");
  assert.equal(result.items[0].causeCategory, "apple_upstream");
  assert.equal(result.items[0].errorContext, "Apple 请求被限流");
  assert.equal(result.items[0].failedStage, "reserving");
  assert.equal(result.items[0].failedOperation, "reserve_alias");
  assert.equal(result.items[0].httpStatus, 429);
  assert.equal(result.items[0].retryable, true);
  assert.equal(result.items[0].elapsedMs, 3821);
  assert.equal(result.items[0].scheduleAction, "continue");
  assert.equal(result.hasMore, true);
  assert.equal(result.nextBeforeId, null);
  assert.equal(result.total, 481);
  assert.equal(result.limit, 50);
  assert.equal(result.offset, 100);
});

test("sync run logs follow every cursor without inheriting list filters", async () => {
  const requests = [];
  const pages = [
    {
      items: [
        { id: 6, created_at: "2026-08-09T08:00:06Z", attributes: { sync_run_id: "run-42", sync_event: "run_failed" } },
        { id: 5, created_at: "2026-08-09T08:00:05Z", attributes: { sync_run_id: "run-42", sync_event: "progress" } },
      ],
      has_more: true,
      next_before_id: 5,
    },
    {
      items: [
        { id: 4, created_at: "2026-08-09T08:00:04Z", attributes: { sync_run_id: "run-42", sync_event: "progress" } },
        { id: 3, created_at: "2026-08-09T08:00:03Z", attributes: { sync_run_id: "run-42", sync_event: "progress" } },
      ],
      has_more: true,
      next_before_id: 3,
    },
    {
      items: [
        { id: 2, created_at: "2026-08-09T08:00:02Z", attributes: { sync_run_id: "run-42", sync_event: "progress" } },
        { id: 1, created_at: "2026-08-09T08:00:01Z", attributes: { sync_run_id: "run-42", sync_event: "run_started" } },
      ],
      has_more: false,
      next_before_id: 0,
    },
  ];

  globalThis.fetch = async (url) => {
    requests.push(new URL(url, "https://admin.invalid"));
    return jsonResponse(pages.shift());
  };

  const result = await getRuntimeLogRun(" run-42 ", { accountId: 12 });

  assert.deepEqual(result.map((item) => item.id), [1, 2, 3, 4, 5, 6]);
  assert.equal(result[0].syncEvent, "run_started");
  assert.equal(result.at(-1).syncEvent, "run_failed");
  assert.equal(requests.length, 3);
  assert.deepEqual(Object.fromEntries(requests[0].searchParams), {
    account_id: "12",
    sync_run_id: "run-42",
    limit: "1000",
  });
  assert.equal(requests[1].searchParams.get("before_id"), "5");
  assert.equal(requests[2].searchParams.get("before_id"), "3");
  for (const request of requests) {
    assert.equal(request.searchParams.has("level"), false);
    assert.equal(request.searchParams.has("query"), false);
  }
});

test("automatic creation run logs follow every cursor with their own run filter", async () => {
  const requests = [];
  const pages = [
    {
      items: [
        {
          id: 4,
          created_at: "2026-08-09T08:00:04Z",
          attributes: {
            auto_create_run_id: "auto-run-42",
            auto_create_event: "run_failed",
          },
        },
        {
          id: 3,
          created_at: "2026-08-09T08:00:03Z",
          attributes: {
            auto_create_run_id: "auto-run-42",
            auto_create_event: "stage_started",
          },
        },
      ],
      has_more: true,
      next_before_id: 3,
    },
    {
      items: [
        {
          id: 2,
          created_at: "2026-08-09T08:00:02Z",
          attributes: {
            auto_create_run_id: "auto-run-42",
            auto_create_event: "stage_started",
          },
        },
        {
          id: 1,
          created_at: "2026-08-09T08:00:01Z",
          attributes: {
            auto_create_run_id: "auto-run-42",
            auto_create_event: "run_started",
          },
        },
      ],
      has_more: false,
      next_before_id: 0,
    },
  ];

  globalThis.fetch = async (url) => {
    requests.push(new URL(url, "https://admin.invalid"));
    return jsonResponse(pages.shift());
  };

  const result = await getAutoCreateLogRun(" auto-run-42 ", {
    accountId: 12,
  });

  assert.deepEqual(result.map((item) => item.id), [1, 2, 3, 4]);
  assert.equal(result[0].autoCreateEvent, "run_started");
  assert.equal(result.at(-1).autoCreateEvent, "run_failed");
  assert.equal(requests.length, 2);
  assert.deepEqual(Object.fromEntries(requests[0].searchParams), {
    account_id: "12",
    auto_create_run_id: "auto-run-42",
    limit: "1000",
  });
  assert.equal(requests[1].searchParams.get("before_id"), "3");
  for (const request of requests) {
    assert.equal(request.searchParams.has("sync_run_id"), false);
    assert.equal(request.searchParams.has("level"), false);
    assert.equal(request.searchParams.has("query"), false);
  }
});
