import { apiRequest } from "./client.js";
import {
  buildRuntimeLogQuery,
  chronologicalRuntimeLogs,
  mergeRuntimeLogs,
  normalizeRuntimeLogPage,
} from "../utils/runtimeLogs.js";
import {
  DEFAULT_PAGE_SIZE,
  MAX_PAGE_SIZE,
} from "../utils/pagination.js";
import { normalizeIMAPEndpoint } from "../utils/imap.js";
import { ALIAS_DELETION_OPERATION_LABELS } from "../utils/aliasDeletionJob.js";

function firstDefined(object, ...keys) {
  for (const key of keys) {
    if (object && object[key] !== undefined) {
      return object[key];
    }
  }
  return undefined;
}

function listFrom(data, ...keys) {
  if (Array.isArray(data)) {
    return data;
  }
  for (const key of keys) {
    if (Array.isArray(data?.[key])) {
      return data[key];
    }
  }
  return [];
}

function integerAtLeast(value, minimum, fallback) {
  const number = Number(value);
  return Number.isFinite(number) && number >= minimum
    ? Math.trunc(number)
    : fallback;
}

function normalizeListPage(data, normalizer, keys = [], options = {}) {
  const rawItems = listFrom(data, ...keys);
  const pagination =
    data && !Array.isArray(data) && typeof data.pagination === "object"
      ? data.pagination || {}
      : {};
  const offset = integerAtLeast(
    firstDefined(pagination, "offset", "Offset") ??
      firstDefined(data, "offset", "Offset"),
    0,
    integerAtLeast(options.offset, 0, 0),
  );
  const limit = integerAtLeast(
    firstDefined(pagination, "limit", "Limit") ??
      firstDefined(data, "limit", "Limit"),
    1,
    integerAtLeast(options.limit, 1, rawItems.length || DEFAULT_PAGE_SIZE),
  );
  const explicitTotal = Number(
    firstDefined(pagination, "total", "Total") ??
      firstDefined(data, "total", "Total"),
  );
  const explicitHasMore =
    firstDefined(pagination, "has_more", "hasMore", "HasMore") ??
    firstDefined(data, "has_more", "hasMore", "HasMore");
  const total =
    Number.isFinite(explicitTotal) && explicitTotal >= 0
      ? Math.trunc(explicitTotal)
      : offset + rawItems.length + (explicitHasMore ? 1 : 0);

  return {
    items: rawItems.map(normalizer),
    total,
    limit,
    offset,
    hasMore:
      explicitHasMore === undefined
        ? offset + rawItems.length < total
        : Boolean(explicitHasMore),
  };
}

function listQuery(options = {}, extra = {}) {
  const parameters = new URLSearchParams();
  const limit = Math.min(
    MAX_PAGE_SIZE,
    integerAtLeast(options.limit, 1, DEFAULT_PAGE_SIZE),
  );
  const offset = Math.min(1_000_000, integerAtLeast(options.offset, 0, 0));
  parameters.set("limit", String(limit));
  parameters.set("offset", String(offset));
  for (const [key, rawValue] of Object.entries(extra)) {
    const value = String(rawValue ?? "").trim();
    if (value) parameters.set(key, value);
  }
  return parameters.toString();
}

export function normalizeAccount(raw = {}) {
  const lastSyncError =
    firstDefined(raw, "last_sync_error", "lastSyncError", "LastSyncError") ||
    "";
  const lastSyncErrorLog = firstDefined(
    raw,
    "last_sync_error_log",
    "lastSyncErrorLog",
    "LastSyncErrorLog",
  );

  const imapEndpoint = normalizeIMAPEndpoint(
    firstDefined(raw, "imap_host", "imapHost", "IMAPHost"),
    firstDefined(raw, "imap_port", "imapPort", "IMAPPort"),
  );

  return {
    id: firstDefined(raw, "id", "ID"),
    name: firstDefined(raw, "name", "Name") || "",
    email: firstDefined(raw, "email", "Email") || "",
    mailboxType:
      firstDefined(
        raw,
        "mailbox_type",
        "mailboxType",
        "account_type",
        "accountType",
        "provider",
        "Provider",
        "MailboxType",
      ) || "icloud",
    provider:
      firstDefined(
        raw,
        "provider",
        "Provider",
        "mailbox_type",
        "mailboxType",
      ) || "icloud",
    emailSuffix:
      firstDefined(raw, "email_suffix", "emailSuffix", "EmailSuffix") || "",
    imapHost: imapEndpoint.host,
    imapPort: imapEndpoint.port,
    imapUsername:
      firstDefined(raw, "imap_username", "imapUsername", "IMAPUsername") ||
      "",
    enabled: Boolean(firstDefined(raw, "enabled", "Enabled")),
    mailTransport: firstDefined(raw, "mail_transport", "mailTransport") === "webmail" ? "webmail" : "imap",
    lastSyncStatus:
      firstDefined(raw, "last_sync_status", "lastSyncStatus", "LastSyncStatus") ||
      "pending",
    lastSyncError,
    lastSyncErrorLog:
      lastSyncErrorLog === undefined ? lastSyncError : lastSyncErrorLog || "",
    lastSyncedAt:
      firstDefined(raw, "last_synced_at", "lastSyncedAt", "LastSyncedAt") ||
      null,
    syncProgress: normalizeSyncProgress(
      firstDefined(raw, "sync_progress", "syncProgress", "SyncProgress"),
    ),
    aliasCount:
      Number(firstDefined(raw, "alias_count", "aliasCount", "AliasCount")) || 0,
  };
}

function normalizeSyncProgress(raw) {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return null;
  }

  const percentageRaw = firstDefined(raw, "percentage", "Percentage");
  const percentageValue =
    typeof percentageRaw === "string" ? percentageRaw.trim() : percentageRaw;
  const percentageNumber =
    percentageValue === undefined ||
    percentageValue === null ||
    percentageValue === ""
      ? Number.NaN
      : Number(percentageValue);

  return {
    active: Boolean(firstDefined(raw, "active", "Active")),
    source: firstDefined(raw, "source", "Source") || "",
    stage: firstDefined(raw, "stage", "Stage") || "",
    percentage: Number.isFinite(percentageNumber)
      ? Math.min(100, Math.max(0, percentageNumber))
      : null,
    startedAt:
      firstDefined(raw, "started_at", "startedAt", "StartedAt") || null,
    updatedAt:
      firstDefined(raw, "updated_at", "updatedAt", "UpdatedAt") || null,
  };
}

export function normalizeAlias(raw = {}) {
  const enabled = Boolean(firstDefined(raw, "enabled", "Enabled"));
  const configuredEnabled = Boolean(
    firstDefined(raw, "configured_enabled", "configuredEnabled", "ConfiguredEnabled") ?? enabled,
  );
  const accountEnabled = Boolean(
    firstDefined(raw, "account_enabled", "accountEnabled", "AccountEnabled") ?? true,
  );
  const lastSyncError =
    firstDefined(raw, "last_sync_error", "lastSyncError", "LastSyncError") ||
    "";
  const lastSyncErrorLog = firstDefined(
    raw,
    "last_sync_error_log",
    "lastSyncErrorLog",
    "LastSyncErrorLog",
  );
  const groupIDRaw = firstDefined(raw, "group_id", "groupId", "GroupID");
  const groupIDNumber = Number(groupIDRaw);

  return {
    id: firstDefined(raw, "id", "ID"),
    accountId: firstDefined(raw, "account_id", "accountId", "AccountID"),
    accountEmail:
      firstDefined(raw, "account_email", "accountEmail", "AccountEmail") || "",
    address: firstDefined(raw, "address", "Address") || "",
    label: firstDefined(raw, "label", "Label") || "",
    groupId:
      groupIDRaw === null || groupIDRaw === undefined || !Number.isFinite(groupIDNumber) || groupIDNumber < 1
        ? null
        : Math.trunc(groupIDNumber),
    groupName:
      firstDefined(raw, "group_name", "groupName", "GroupName") || "",
    apiKey: firstDefined(raw, "api_key", "apiKey", "APIKey") || "",
    apiKeyPrefix:
      firstDefined(raw, "api_key_prefix", "apiKeyPrefix", "APIKeyPrefix") ||
      "",
    imapPassword:
      firstDefined(raw, "imap_password", "imapPassword", "IMAPPassword") || "",
    clientId: firstDefined(raw, "client_id", "clientId", "ClientID") || "",
    refreshToken:
      firstDefined(raw, "refresh_token", "refreshToken", "RefreshToken") || "",
    otpUrlPath:
      firstDefined(
        raw,
        "otp_url_path",
        "otpUrlPath",
        "OTPURLPath",
      ) || "",
    directLinkPath:
      firstDefined(
        raw,
        "direct_link_path",
        "directLinkPath",
        "DirectLinkPath",
        "legacy_direct_link_path",
        "legacyDirectLinkPath",
        "LegacyDirectLink",
      ) || "",
    legacyDirectLinkPath:
      firstDefined(
        raw,
        "legacy_direct_link_path",
        "legacyDirectLinkPath",
        "LegacyDirectLink",
        "direct_link_path",
        "directLinkPath",
        "DirectLinkPath",
      ) || "",
    credentialMode:
      firstDefined(raw, "credential_mode", "credentialMode", "CredentialMode") ||
      "",
    credentialVersion:
      Number(
        firstDefined(
          raw,
          "credential_version",
          "credentialVersion",
          "CredentialVersion",
        ),
      ) || 0,
    enabled,
    configuredEnabled,
    accountEnabled,
    lastSyncStatus:
      firstDefined(raw, "last_sync_status", "lastSyncStatus", "LastSyncStatus") ||
      "pending",
    lastSyncError,
    lastSyncErrorLog:
      lastSyncErrorLog === undefined ? lastSyncError : lastSyncErrorLog || "",
    lastSyncedAt:
      firstDefined(raw, "last_synced_at", "lastSyncedAt", "LastSyncedAt") ||
      null,
    lastAccessedAt:
      firstDefined(raw, "last_accessed_at", "lastAccessedAt", "LastAccessedAt") ||
      null,
    latestReceivedAt:
      firstDefined(
        raw,
        "latest_received_at",
        "latestReceivedAt",
        "LatestReceivedAt",
      ) || null,
  };
}

export function normalizeAppleSession(raw) {
  if (!raw || typeof raw !== "object") {
    return null;
  }
  return {
    status:
      firstDefined(raw, "status", "Status", "auth_state", "authState", "AuthState") ||
      (firstDefined(raw, "authenticated", "Authenticated")
        ? "authenticated"
        : ""),
    appleId:
      firstDefined(raw, "apple_id", "appleId", "AppleID") || "",
    region: firstDefined(raw, "region", "Region") || "global",
    authenticatedAt:
      firstDefined(
        raw,
        "authenticated_at",
        "authenticatedAt",
        "AuthenticatedAt",
      ) || null,
    expiresAt:
      firstDefined(raw, "expires_at", "expiresAt", "ExpiresAt") || null,
  };
}

export function normalizeAutoCreation(raw = {}) {
  const value = raw && typeof raw === "object" ? raw : {};
  const plannedTimesRaw = firstDefined(
    value,
    "planned_times",
    "plannedTimes",
    "PlannedTimes",
  );
  const plannedTimes = Array.isArray(plannedTimesRaw)
    ? plannedTimesRaw
        .filter((planned) => planned !== null && planned !== undefined)
        .map((planned) => String(planned).trim())
        .filter(Boolean)
    : [];
  const plannedAt =
    firstDefined(value, "planned_at", "plannedAt", "PlannedAt") ||
    plannedTimes[0] ||
    null;
  const recentCreatedCountRaw = firstDefined(
    value,
    "recent_created_count",
    "recentCreatedCount",
    "RecentCreatedCount",
  );
  const recentCreatedCount =
    Number.isSafeInteger(recentCreatedCountRaw) && recentCreatedCountRaw >= 0
      ? recentCreatedCountRaw
      : null;
  const todayCreatedCountRaw = firstDefined(
    value,
    "today_created_count",
    "todayCreatedCount",
    "TodayCreatedCount",
  );
  const todayCreatedCount =
    Number.isSafeInteger(todayCreatedCountRaw) && todayCreatedCountRaw >= 0
      ? todayCreatedCountRaw
      : null;
  return {
    enabled: Boolean(firstDefined(value, "enabled", "Enabled")),
    status: firstDefined(value, "status", "Status") || "",
    nextRunAt:
      firstDefined(value, "next_run_at", "nextRunAt", "NextRunAt") || null,
    plannedAt,
    plannedTimes,
    recentCreatedCount,
    todayCreatedCount,
    recentCreatedSince:
      firstDefined(value, "recent_created_since", "recentCreatedSince", "RecentCreatedSince") || null,
    todayCreatedSince:
      firstDefined(value, "today_created_since", "todayCreatedSince", "TodayCreatedSince") || null,
    lastAttemptedAt:
      firstDefined(
        value,
        "last_attempted_at",
        "lastAttemptedAt",
        "LastAttemptedAt",
      ) || null,
    lastCreatedAt:
      firstDefined(
        value,
        "last_created_at",
        "lastCreatedAt",
        "LastCreatedAt",
      ) || null,
    lastAliasAddress:
      firstDefined(
        value,
        "last_alias_address",
        "lastAliasAddress",
        "LastAliasAddress",
      ) || "",
    lastError:
      firstDefined(value, "last_error", "lastError", "LastError") || "",
  };
}

export function normalizeAuditLog(raw = {}) {
  return {
    id: firstDefined(raw, "id", "ID"),
    username: firstDefined(raw, "username", "Username") || "",
    action: firstDefined(raw, "action", "Action") || "",
    resourceType:
      firstDefined(raw, "resource_type", "resourceType", "ResourceType") || "",
    resourceId:
      firstDefined(raw, "resource_id", "resourceId", "ResourceID") || "",
    result: firstDefined(raw, "result", "Result") || "",
    requestId:
      firstDefined(raw, "request_id", "requestId", "RequestID") || "",
    createdAt:
      firstDefined(raw, "created_at", "createdAt", "CreatedAt") || null,
  };
}

function authData(data = {}) {
  return {
    username: data.admin?.username || data.username || "",
    csrfToken: data.csrf_token || data.csrfToken || "",
    expiresAt: data.expires_at || data.expiresAt || null,
  };
}

export async function getLoginCsrf() {
  const data = await apiRequest("/auth/csrf", { handleUnauthorized: false });
  return data?.csrf_token || data?.csrfToken || "";
}

export async function login(username, password, csrfToken) {
  const data = await apiRequest("/auth/login", {
    method: "POST",
    body: { username, password },
    csrfToken,
    handleUnauthorized: false,
  });
  return authData(data);
}

export async function getSession() {
  return authData(
    await apiRequest("/auth/session", { handleUnauthorized: false }),
  );
}

export function logout(csrfToken) {
  return apiRequest("/auth/logout", { method: "POST", csrfToken });
}

export function updatePassword(payload, csrfToken) {
  return apiRequest("/auth/password", {
    method: "PUT",
    body: payload,
    csrfToken,
  });
}

export function getAccounts(options = {}) {
  return getAllAccounts(options);
}

export async function getAccountPage(options = {}) {
  const query = listQuery(options, { query: options.query });
  const data = await apiRequest(`/accounts?${query}`, {
    signal: options.signal,
  });
  return normalizeListPage(data, normalizeAccount, ["accounts", "items"], options);
}

async function collectOffsetPages(fetchPage, options = {}) {
  const items = [];
  let offset = 0;
  let total = 0;
  const maximumPages = 10000;

  for (let pageNumber = 0; pageNumber < maximumPages; pageNumber += 1) {
    const page = await fetchPage({
      ...options,
      limit: MAX_PAGE_SIZE,
      offset,
    });
    const pageItems = Array.isArray(page?.items) ? page.items : [];
    items.push(...pageItems);
    const reportedTotal = Number(page?.total);
    if (Number.isFinite(reportedTotal) && reportedTotal >= 0) {
      total = Math.trunc(reportedTotal);
    }

    const pageOffset = Number(page?.offset);
    const nextOffset =
      Number.isFinite(pageOffset) && pageOffset >= 0
        ? Math.trunc(pageOffset) + pageItems.length
        : offset + pageItems.length;
    if (
      !page?.hasMore ||
      pageItems.length === 0 ||
      nextOffset <= offset ||
      (total > 0 && nextOffset >= total)
    ) {
      break;
    }
    offset = nextOffset;
  }

  return items;
}

export function getAllAccounts(options = {}) {
  return collectOffsetPages(getAccountPage, options);
}

export async function getMailGroups() {
  const data = await apiRequest("/groups");
  return listFrom(data, "groups", "items").map(normalizeMailGroup);
}

export async function createMailGroup(name, csrfToken) {
  const data = await apiRequest("/groups", {
    method: "POST",
    body: { name },
    csrfToken,
  });
  return normalizeMailGroup(data?.group || data || {});
}

export async function updateMailGroup(id, name, csrfToken) {
  const data = await apiRequest(`/groups/${encodeURIComponent(id)}`, {
    method: "PATCH",
    body: { name },
    csrfToken,
  });
  return normalizeMailGroup(data?.group || data || {});
}

export function deleteMailGroup(id, csrfToken) {
  return apiRequest(`/groups/${encodeURIComponent(id)}`, {
    method: "DELETE",
    csrfToken,
  });
}

export async function getAccount(id, options = {}) {
  const query = listQuery(options);
  const data = await apiRequest(
    `/accounts/${encodeURIComponent(id)}?${query}`,
    { signal: options.signal },
  );
  return normalizeAccountDetail(data, options);
}

function normalizeAccountDetail(data = {}, options = {}) {
  const accountRaw = data?.account || data || {};
  const account = normalizeAccount(accountRaw);
  if (firstDefined(data, "mail_transport", "mailTransport") !== undefined) {
    account.mailTransport = firstDefined(data, "mail_transport", "mailTransport") === "webmail" ? "webmail" : "imap";
  }
  const aliasPage = normalizeListPage(
    data,
    normalizeAlias,
    ["aliases"],
    options,
  );
  const paginationRaw =
    data && !Array.isArray(data) && typeof data.pagination === "object"
      ? data.pagination || {}
      : {};
  const explicitTotal = Number(
    firstDefined(paginationRaw, "total", "Total") ??
      firstDefined(data, "total", "Total"),
  );
  const explicitHasMore =
    firstDefined(paginationRaw, "has_more", "hasMore", "HasMore") ??
    firstDefined(data, "has_more", "hasMore", "HasMore");
  const total =
    Number.isFinite(explicitTotal) && explicitTotal >= 0
      ? Math.trunc(explicitTotal)
      : Math.max(account.aliasCount, aliasPage.total);
  account.aliasCount = total;

  return {
    account,
    aliases: aliasPage.items,
    pagination: {
      total,
      limit: aliasPage.limit,
      offset: aliasPage.offset,
      hasMore:
        explicitHasMore === undefined
          ? aliasPage.offset + aliasPage.items.length < total
          : Boolean(explicitHasMore),
    },
    appleSession: normalizeAppleSession(
      firstDefined(data, "apple_session", "appleSession", "AppleSession"),
    ),
    autoCreation: normalizeAutoCreation(
      firstDefined(data, "auto_creation", "autoCreation", "AutoCreation"),
    ),
    syncPending: Boolean(
      firstDefined(data, "sync_pending", "syncPending", "SyncPending"),
    ),
  };
}

export async function createAccount(payload, csrfToken) {
  const data = await apiRequest("/accounts", {
    method: "POST",
    body: payload,
    csrfToken,
  });
  return normalizeAccount(data?.account || data || {});
}

export async function updateAccount(id, payload, csrfToken) {
  const data = await apiRequest(`/accounts/${encodeURIComponent(id)}`, {
    method: "PUT",
    body: payload,
    csrfToken,
  });
  return normalizeAccount(data?.account || data || {});
}

export async function updateAccountMailTransport(id, transport, csrfToken) {
  const data = await apiRequest(`/accounts/${encodeURIComponent(id)}/mail-transport`, {
    method: "PUT",
    body: { transport },
    csrfToken,
  });
  return data?.transport === "webmail" ? "webmail" : "imap";
}

export function normalizeMailGroup(raw = {}) {
  return {
    id: firstDefined(raw, "id", "ID"),
    name: firstDefined(raw, "name", "Name") || "",
    aliasCount:
      Number(firstDefined(raw, "alias_count", "aliasCount", "AliasCount")) ||
      0,
    createdAt:
      firstDefined(raw, "created_at", "createdAt", "CreatedAt") || null,
    updatedAt:
      firstDefined(raw, "updated_at", "updatedAt", "UpdatedAt") || null,
  };
}

function normalizeRandomAliasResult(data = {}) {
  const rawCreated = listFrom(data, "created", "items");
  const rawAliases = listFrom(data, "aliases");
  const created = rawCreated.map((item) => {
    const rawAlias = firstDefined(item, "alias", "Alias") || item;
    const alias = normalizeAlias(
      typeof rawAlias === "string" ? { address: rawAlias } : rawAlias,
    );
    return {
      alias,
      apiKey:
        firstDefined(item, "api_key", "apiKey", "APIKey") || alias.apiKey,
      otpUrlPath:
        firstDefined(
          item,
          "otp_url_path",
          "otpUrlPath",
          "OTPURLPath",
          "mail_api_direct_link",
          "mailApiDirectLink",
          "MailAPIDirectLink",
        ) || alias.otpUrlPath || alias.directLinkPath,
    };
  });
  return {
    created,
    aliases: (rawAliases.length ? rawAliases : created.map((item) => item.alias)).map(
      normalizeAlias,
    ),
    count:
      Number(firstDefined(data, "count", "Count")) || created.length,
  };
}

export async function createRandomAliases(accountId, payload, csrfToken) {
  const data = await apiRequest(
    `/accounts/${encodeURIComponent(accountId)}/aliases/random`,
    {
      method: "POST",
      body: payload,
      csrfToken,
    },
  );
  return normalizeRandomAliasResult(data?.data || data || {});
}

function normalizeAliasCreationJob(raw = {}) {
  const job = raw && Object.hasOwn(raw, "job") ? raw.job : raw;
  return job ? {
    ...job,
    target: Number(job.target) || 0,
    completed: Number(job.completed) || 0,
    status: job.status || "",
    last_error: job.last_error || "",
    next_run_at: job.next_run_at || null,
    entries: Array.isArray(job.entries) ? job.entries : [],
  } : null;
}

export async function createAliasCreationJob(accountId, payload, csrfToken) {
  const data = await apiRequest(`/accounts/${encodeURIComponent(accountId)}/aliases/creation-job`, { method: "POST", body: payload, csrfToken });
  return normalizeAliasCreationJob(data?.data || data);
}
export async function getAliasCreationJob(accountId, csrfToken) {
  const data = await apiRequest(`/accounts/${encodeURIComponent(accountId)}/aliases/creation-job`, { method: "GET", csrfToken });
  return normalizeAliasCreationJob(data?.data || data);
}
export async function stopAliasCreationJob(accountId, csrfToken) {
  const data = await apiRequest(`/accounts/${encodeURIComponent(accountId)}/aliases/creation-job/stop`, { method: "POST", body: {}, csrfToken });
  return normalizeAliasCreationJob(data?.data || data);
}

export function deleteAccount(id, csrfToken) {
  return apiRequest(`/accounts/${encodeURIComponent(id)}`, {
    method: "DELETE",
    csrfToken,
  });
}

export async function getAppleAccountAuth(id, csrfToken) {
  const data = await apiRequest(`/accounts/${encodeURIComponent(id)}/apple-account-auth`, { method: "GET", csrfToken });
  return appleSessionResult(data?.data || data || {});
}
export async function loginAppleAccountAuth(id, payload, csrfToken) {
  const data = await apiRequest(`/accounts/${encodeURIComponent(id)}/apple-account-auth`, { method: "POST", body: payload, csrfToken });
  return appleSessionResult(data?.data || data || {});
}
export async function verifyAppleAccountAuth(id, payload, csrfToken) {
  const data = await apiRequest(`/accounts/${encodeURIComponent(id)}/apple-account-auth/verify`, { method: "POST", body: payload, csrfToken });
  return appleSessionResult(data?.data || data || {});
}
export function deleteAppleAccountAuth(id, csrfToken) {
  return apiRequest(`/accounts/${encodeURIComponent(id)}/apple-account-auth`, { method: "DELETE", csrfToken });
}

export async function syncAccount(id, csrfToken) {
  const data = await apiRequest(`/accounts/${encodeURIComponent(id)}/sync`, {
    method: "POST",
    csrfToken,
  });
  return normalizeAccountDetail(data);
}

function appleSessionResult(data = {}) {
  const appleSession = normalizeAppleSession(
    firstDefined(data, "apple_session", "appleSession", "AppleSession"),
  );
  return {
    status:
      firstDefined(data, "status", "Status") || appleSession?.status || "",
    appleSession,
    flow: firstDefined(data, "flow", "Flow") || "",
    challengeId:
      firstDefined(data, "challenge_id", "challengeId", "ChallengeID") || "",
  };
}

export async function loginAppleSession(accountId, payload, csrfToken) {
  const data = await apiRequest(
    `/accounts/${encodeURIComponent(accountId)}/apple-auth`,
    {
      method: "POST",
      body: payload,
      csrfToken,
    },
  );
  return appleSessionResult(data);
}

export async function verifyAppleSession(accountId, payload, csrfToken) {
  const data = await apiRequest(
    `/accounts/${encodeURIComponent(accountId)}/apple-auth/verify`,
    {
      method: "POST",
      body: payload,
      csrfToken,
    },
  );
  return appleSessionResult(data);
}

export function deleteAppleSession(accountId, csrfToken) {
  return apiRequest(
    `/accounts/${encodeURIComponent(accountId)}/apple-auth`,
    {
      method: "DELETE",
      csrfToken,
    },
  );
}

function normalizeSyncSummary(raw = {}) {
  return {
    total:
      Number(firstDefined(raw, "total", "Total", "discovered", "Discovered")) ||
      0,
    createdCount:
      Number(
        firstDefined(
          raw,
          "created_count",
          "createdCount",
          "CreatedCount",
          "created",
          "Created",
        ),
      ) ||
      0,
    existingCount:
      Number(
        firstDefined(
          raw,
          "existing_count",
          "existingCount",
          "ExistingCount",
          "existing",
          "Existing",
        ),
      ) || 0,
    inactiveCount:
      Number(
        firstDefined(
          raw,
          "inactive_count",
          "inactiveCount",
          "InactiveCount",
          "inactive",
          "Inactive",
        ),
      ) || 0,
    importedDisabledCount:
      Number(
        firstDefined(
          raw,
          "imported_disabled_count",
          "importedDisabledCount",
          "ImportedDisabledCount",
          "imported_disabled",
          "ImportedDisabled",
          "filtered_out_count",
          "filteredOutCount",
          "FilteredOutCount",
        ),
      ) || 0,
    conflictCount:
      Number(
        firstDefined(
          raw,
          "conflict_count",
          "conflictCount",
          "ConflictCount",
          "conflicts",
          "Conflicts",
        ),
      ) || 0,
  };
}

function normalizeCreatedAlias(raw = {}) {
  const aliasRaw = firstDefined(raw, "alias", "Alias") || {};
  const alias = normalizeAlias(
    typeof aliasRaw === "string" ? { address: aliasRaw } : aliasRaw,
  );
  return {
    alias,
    apiKey:
      firstDefined(raw, "api_key", "apiKey", "APIKey") || alias.apiKey,
    otpUrlPath:
      firstDefined(
        raw,
        "otp_url_path",
        "otpUrlPath",
        "OTPURLPath",
        "mail_api_direct_link",
        "mailApiDirectLink",
        "MailAPIDirectLink",
      ) || alias.otpUrlPath || alias.directLinkPath,
  };
}

function normalizeAutoCreationResult(data) {
  const nested = firstDefined(
    data,
    "auto_creation",
    "autoCreation",
    "AutoCreation",
  );
  return normalizeAutoCreation(nested === undefined ? data : nested);
}

export async function setAliasAutoCreation(accountId, enabled, csrfToken) {
  const data = await apiRequest(
    `/accounts/${encodeURIComponent(accountId)}/aliases/auto-create`,
    {
      method: "PUT",
      body: { enabled },
      csrfToken,
    },
  );
  return normalizeAutoCreationResult(data);
}

export async function syncAccountAliases(accountId, csrfToken) {
  const data =
    (await apiRequest(
      `/accounts/${encodeURIComponent(accountId)}/aliases/sync`,
      {
        method: "POST",
        csrfToken,
      },
    )) || {};
  return {
    ...normalizeAccountDetail(data),
    summary: normalizeSyncSummary(data.summary),
    created: listFrom(data, "created").map(normalizeCreatedAlias),
    detailStale: Boolean(
      firstDefined(data, "detail_stale", "detailStale", "DetailStale"),
    ),
  };
}

function aliasMutationResult(data = {}) {
  const alias = normalizeAlias(data.alias || data);
  return {
    alias,
    apiKey:
      firstDefined(data, "api_key", "apiKey", "APIKey") || alias.apiKey,
    otpUrlPath:
      firstDefined(
        data,
        "otp_url_path",
        "otpUrlPath",
        "OTPURLPath",
        "mail_api_direct_link",
        "mailApiDirectLink",
        "MailAPIDirectLink",
      ) || alias.otpUrlPath || alias.directLinkPath,
  };
}

export async function createAlias(accountId, payload, csrfToken) {
  return aliasMutationResult(
    await apiRequest(`/accounts/${encodeURIComponent(accountId)}/aliases`, {
      method: "POST",
      body: payload,
      csrfToken,
    }),
  );
}

export async function createAliasNow(accountId, csrfToken, channel = "auto") {
  const data = await apiRequest(
    `/accounts/${encodeURIComponent(accountId)}/aliases/create-now`,
    { method: "POST", body: { channel }, csrfToken },
  );
  return normalizeAlias(data?.alias || data || {});
}

export function getAliases(accountId = "", options = {}) {
  return getAllAliases(accountId, options);
}

export async function getAliasPage(accountId = "", options = {}) {
  const query = listQuery(options, {
    account_id: accountId,
    group_id: options.groupId,
    query: options.query,
    without_latest_mail: options.withoutLatestMail === true ? "true" : "",
    with_latest_mail: options.withLatestMail === true ? "true" : "",
    enabled:
      typeof options.enabled === "boolean" ? String(options.enabled) : "",
  });
  const data = await apiRequest(`/aliases?${query}`, {
    signal: options.signal,
  });
  return normalizeListPage(data, normalizeAlias, ["aliases", "items"], options);
}

export async function moveAliasToGroup(id, groupId, csrfToken) {
  const data = await apiRequest(`/aliases/${encodeURIComponent(id)}/group`, {
    method: "PATCH",
    body: { group_id: groupId == null ? null : Number(groupId) },
    csrfToken,
  });
  return normalizeAlias(data?.alias || data || {});
}

export function moveAliasesToGroup(ids, groupId, csrfToken) {
  return apiRequest("/aliases/group", {
    method: "PATCH",
    body: {
      alias_ids: ids,
      group_id: groupId == null ? null : Number(groupId),
    },
    csrfToken,
  });
}

export function getAllAliases(accountId = "", options = {}) {
  return collectOffsetPages(
    (pageOptions) => getAliasPage(accountId, pageOptions),
    options,
  );
}

export async function rotateAlias(id, csrfToken, credentialMode = "") {
  const operation =
    credentialMode === "v2" ? "rotate-credentials" : "rotate-key";
  return aliasMutationResult(
    await apiRequest(`/aliases/${encodeURIComponent(id)}/${operation}`, {
      method: "POST",
      csrfToken,
    }),
  );
}

function invalidRotationSummaryError() {
  const error = new Error(
    "轮换已提交，但服务返回的汇总无效，请重新登录后检查操作记录。",
  );
  error.code = "ROTATION_RESULT_INVALID";
  return error;
}

function requiredRotationSummaryCount(data, ...keys) {
  const value = firstDefined(data, ...keys);
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) {
    throw invalidRotationSummaryError();
  }
  return value;
}

export async function rotateAllAliasCredentials(currentPassword, csrfToken) {
  const data =
    (await apiRequest("/aliases/rotate-all-credentials", {
      method: "POST",
      body: {
        confirmation: "ROTATE_ALL",
        current_password: currentPassword,
      },
      csrfToken,
    })) || {};
  const summary = {
    total: requiredRotationSummaryCount(data, "total", "Total"),
    rotated: requiredRotationSummaryCount(data, "rotated", "Rotated"),
    migratedLegacy: requiredRotationSummaryCount(
      data,
      "migrated_legacy",
      "migratedLegacy",
      "MigratedLegacy",
    ),
    rotatedV2: requiredRotationSummaryCount(
      data,
      "rotated_v2",
      "rotatedV2",
      "RotatedV2",
    ),
    rotatedPending: requiredRotationSummaryCount(
      data,
      "rotated_pending",
      "rotatedPending",
      "RotatedPending",
    ),
    reauthenticationRequired:
      firstDefined(
        data,
        "reauthentication_required",
        "reauthenticationRequired",
        "ReauthenticationRequired",
      ) === true,
  };
  if (
    summary.rotated !== summary.migratedLegacy + summary.rotatedV2 ||
    summary.total !== summary.rotated ||
    summary.rotatedPending > summary.rotated ||
    !summary.reauthenticationRequired
  ) {
    throw invalidRotationSummaryError();
  }
  return summary;
}

export async function setAliasEnabled(id, enabled, csrfToken) {
  const data = await apiRequest(`/aliases/${encodeURIComponent(id)}`, {
    method: "PATCH",
    body: { enabled },
    csrfToken,
  });
  return normalizeAlias(data?.alias || data || {});
}

export async function updateAliasGroup(id, groupId, csrfToken) {
  const data = await apiRequest(`/aliases/${encodeURIComponent(id)}`, {
    method: "PATCH",
    body: { group_id: groupId == null ? null : Number(groupId) },
    csrfToken,
  });
  return normalizeAlias(data?.alias || data || {});
}

export function deleteAlias(id, csrfToken) {
  return apiRequest(`/aliases/${encodeURIComponent(id)}`, {
    method: "DELETE",
    csrfToken,
  });
}

export async function deleteAliases(ids, csrfToken) {
  const data = await apiRequest("/aliases/batch", {
    method: "DELETE",
    body: { alias_ids: ids },
    csrfToken,
  });
  const rawResults = listFrom(data, "results", "items");
  return {
    requested: integerAtLeast(
      firstDefined(data, "requested", "Requested"),
      0,
      ids.length,
    ),
    deleted: integerAtLeast(
      firstDefined(data, "deleted", "Deleted"),
      0,
      0,
    ),
    failed: integerAtLeast(
      firstDefined(data, "failed", "Failed"),
      0,
      0,
    ),
    results: rawResults.map((raw) => ({
      id: firstDefined(raw, "id", "ID"),
      address: firstDefined(raw, "address", "Address") || "",
      deleted: Boolean(firstDefined(raw, "deleted", "Deleted")),
      code: firstDefined(raw, "code", "Code") || "",
      message: firstDefined(raw, "message", "Message") || "",
      localRetained: Boolean(
        firstDefined(raw, "local_retained", "localRetained", "LocalRetained"),
      ),
    })),
  };
}

function normalizeAliasDeletionWait(raw) {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const accountId = firstDefined(raw, "account_id", "accountId", "AccountID");
  const aliasId = firstDefined(raw, "alias_id", "aliasId", "AliasID");
  const operation = firstDefined(raw, "operation", "Operation");
  const retryAt = firstDefined(raw, "retry_at", "retryAt", "RetryAt");
  const attempt = firstDefined(raw, "attempt", "Attempt");
  const maxAttempts = firstDefined(raw, "max_attempts", "maxAttempts", "MaxAttempts");
  if (![accountId, aliasId].every((id) => Number.isSafeInteger(id) && id >= 0) ||
      (accountId === 0 && aliasId === 0) ||
      typeof operation !== "string" || !Object.hasOwn(ALIAS_DELETION_OPERATION_LABELS, operation) ||
      typeof retryAt !== "string" ||
      !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(retryAt) ||
      !Number.isFinite(Date.parse(retryAt)) ||
      !Number.isSafeInteger(attempt) || attempt < 1 || attempt > 3 || maxAttempts !== 3) {
    return null;
  }

  // Optional metadata is allowlisted; never carry upstream bodies into the view.
  const wait = { accountId, aliasId, operation, retryAt, attempt, maxAttempts };
  const httpStatus = firstDefined(raw, "http_status", "httpStatus", "HTTPStatus");
  const serviceCode = firstDefined(raw, "service_code", "serviceCode", "ServiceCode");
  if (Number.isSafeInteger(httpStatus) && httpStatus >= 100 && httpStatus <= 599) {
    wait.httpStatus = httpStatus;
  }
  if (typeof serviceCode === "string" && /^[A-Za-z0-9_.-]{1,64}$/.test(serviceCode)) {
    wait.serviceCode = serviceCode;
  }
  return wait;
}

export function normalizeAliasDeletionJob(raw) {
  if (raw === null) return null;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    throw Object.assign(new Error("任务查询响应异常，删除结果待确认。"), {
      code: "INVALID_RESPONSE",
    });
  }

  const job = raw.job && typeof raw.job === "object" ? raw.job : raw;
  const rawResults = listFrom(job, "results", "items");
  const count = (...keys) => {
    const value = firstDefined(job, ...keys);
    return Number.isSafeInteger(value) && value >= 0 ? value : null;
  };
  const failed = count("failed", "Failed");
  const deferred = count("deferred", "Deferred");
  const waits = firstDefined(job, "waits", "Waits");
  return {
    jobId: firstDefined(job, "job_id", "jobId", "JobID") || "",
    status: firstDefined(job, "status", "Status") || "",
    requested: count("requested", "Requested"),
    processed: count("processed", "Processed"),
    deleted: count("deleted", "Deleted"),
    failed,
    deferred: deferred !== null && failed !== null && deferred <= failed ? deferred : 0,
    waits: Array.isArray(waits) ? waits.map(normalizeAliasDeletionWait).filter(Boolean) : [],
    results: rawResults.map((rawResult) => ({
      id: firstDefined(rawResult, "id", "ID"),
      address: firstDefined(rawResult, "address", "Address") || "",
      deleted: firstDefined(rawResult, "deleted", "Deleted") === true,
      code: firstDefined(rawResult, "code", "Code") || "",
      message: firstDefined(rawResult, "message", "Message") || "",
      localRetained:
        firstDefined(
          rawResult,
          "local_retained",
          "localRetained",
          "LocalRetained",
        ) === true,
    })),
    requestId: firstDefined(job, "request_id", "requestId", "RequestID") || "",
    createdAt:
      firstDefined(job, "created_at", "createdAt", "CreatedAt") || null,
    updatedAt:
      firstDefined(job, "updated_at", "updatedAt", "UpdatedAt") || null,
  };
}

export async function startAliasDeletionJob(ids, operationId, csrfToken, options = {}) {
  const data = await apiRequest("/aliases/batch?async=1", {
    method: "DELETE",
    body: { alias_ids: ids, operation_id: operationId },
    csrfToken,
    signal: options.signal,
  });
  return normalizeAliasDeletionJob(data);
}

export async function getAliasDeletionJob(jobId, options = {}) {
  const data = await apiRequest(
    `/aliases/batch/jobs/${encodeURIComponent(jobId)}`,
    { signal: options.signal },
  );
  return normalizeAliasDeletionJob(data);
}

export async function getLatestAliasDeletionJob(options = {}) {
  const data = await apiRequest("/aliases/batch/jobs/latest", {
    signal: options.signal,
  });
  return normalizeAliasDeletionJob(data);
}

export async function getAuditLogs(options = {}) {
  const query = listQuery(options);
  const data = await apiRequest(`/audit?${query}`, {
    signal: options.signal,
  });
  return normalizeListPage(
    data,
    normalizeAuditLog,
    ["audit", "audit_logs", "logs", "items"],
    options,
  );
}

export function getAllAuditLogs(options = {}) {
  return collectOffsetPages(getAuditLogs, options);
}

export async function getRuntimeLogs(options = {}) {
  const query = buildRuntimeLogQuery(options);
  const data = await apiRequest(`/logs${query ? `?${query}` : ""}`, {
    signal: options.signal,
  });
  return normalizeRuntimeLogPage(data || {});
}

export function getAllRuntimeLogs(options = {}) {
  return collectOffsetPages(
    (pageOptions) => getRuntimeLogs({ ...options, ...pageOptions }),
    options,
  );
}

async function getRuntimeLogFlow(runFilter, options = {}) {
  if (!Object.values(runFilter).some(Boolean)) return [];
  const maximum = 2000;
  const seenCursors = new Set();
  let beforeId = "";
  let items = [];

  while (items.length < maximum) {
    const page = await getRuntimeLogs({
      accountId: options.accountId,
      beforeId,
      limit: MAX_PAGE_SIZE,
      signal: options.signal,
      ...runFilter,
    });
    items = mergeRuntimeLogs(items, page.items).slice(0, maximum);

    const nextCursor = String(page.nextBeforeId ?? "").trim();
    if (!page.hasMore || !nextCursor || seenCursors.has(nextCursor)) break;
    seenCursors.add(nextCursor);
    beforeId = nextCursor;
  }

  return chronologicalRuntimeLogs(items);
}

export function getRuntimeLogRun(syncRunId, options = {}) {
  const normalizedRunId = String(syncRunId ?? "").trim();
  return getRuntimeLogFlow({ syncRunId: normalizedRunId }, options);
}

export function getAutoCreateLogRun(autoCreateRunId, options = {}) {
  const normalizedRunId = String(autoCreateRunId ?? "").trim();
  return getRuntimeLogFlow({ autoCreateRunId: normalizedRunId }, options);
}
