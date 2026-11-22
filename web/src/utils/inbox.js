import { formatTime } from "./format.js";

export const RECENT_INBOX_LOOKUPS_STORAGE_KEY = "icloud-admin-recent-inbox-lookups";
export const MAX_RECENT_INBOX_LOOKUPS = 8;

/**
 * Parse an email address that may include a +tag subaddress (e.g. foo+chatgpt@icloud.com).
 */
export function parsePlusTagAddress(input) {
  const normalized = String(input ?? "").trim().toLowerCase();
  if (!normalized) {
    return { normalized: "", rootAddress: "", tag: "" };
  }

  const atIndex = normalized.lastIndexOf("@");
  if (atIndex <= 0 || atIndex === normalized.length - 1) {
    return { normalized, rootAddress: normalized, tag: "" };
  }

  const local = normalized.slice(0, atIndex);
  const domain = normalized.slice(atIndex + 1);
  const plusIndex = local.indexOf("+");
  if (plusIndex <= 0) {
    return { normalized, rootAddress: normalized, tag: "" };
  }

  const baseLocal = local.slice(0, plusIndex);
  const tag = local.slice(plusIndex + 1);
  return {
    normalized,
    rootAddress: `${baseLocal}@${domain}`,
    tag,
  };
}

/**
 * Format a mail party list (`[{ name, email }]`) for display.
 */
export function formatMailParty(list) {
  if (!list) return "";
  const items = Array.isArray(list) ? list : [list];
  const formatted = [];

  for (const item of items) {
    if (!item) continue;
    if (typeof item === "string") {
      const text = item.trim();
      if (text) formatted.push(text);
      continue;
    }
    const name = String(item.name ?? "").trim();
    const email = String(item.email ?? "").trim();
    if (name && email && name.toLowerCase() !== email.toLowerCase()) {
      formatted.push(`${name} <${email}>`);
    } else if (email) {
      formatted.push(email);
    } else if (name) {
      formatted.push(name);
    }
  }

  return formatted.join(", ");
}

/**
 * Format byte sizes into a human-readable string.
 */
export function formatByteSize(bytes) {
  const value = Number(bytes);
  if (!Number.isFinite(value) || value < 0) return "-";
  if (value < 1024) return `${Math.trunc(value)} B`;

  const units = ["KB", "MB", "GB", "TB"];
  let size = value / 1024;
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024;
    unitIndex += 1;
  }

  const formatted = size >= 100 ? Math.round(size).toString() : size.toFixed(1).replace(/\.0$/, "");
  return `${formatted} ${units[unitIndex]}`;
}

/**
 * Format a mail timestamp relative to `now` in Chinese.
 */
export function formatRelativeMailTime(iso, now = Date.now()) {
  if (!iso) return "-";
  const date = iso instanceof Date ? iso : new Date(iso);
  const ms = date.getTime();
  if (Number.isNaN(ms)) return "-";

  const nowMs = now instanceof Date ? now.getTime() : Number(now);
  const diffSeconds = Math.floor((nowMs - ms) / 1000);

  if (diffSeconds < -60) {
    return formatTime(date);
  }
  if (diffSeconds < 60) {
    return "刚刚";
  }
  if (diffSeconds < 3600) {
    return `${Math.floor(diffSeconds / 60)} 分钟前`;
  }
  if (diffSeconds < 86400) {
    return `${Math.floor(diffSeconds / 3600)} 小时前`;
  }
  if (diffSeconds < 7 * 86400) {
    return `${Math.floor(diffSeconds / 86400)} 天前`;
  }
  return formatTime(date);
}

const CONTENT_STATE_LABELS = {
  available: "正文完整可用",
  evicted: "正文已按容量淘汰（保留主题、发件人与验证码）",
  oversized: "正文超出归档大小上限，仅保留元数据与验证码",
  missing: "暂未缓存正文内容，可尝试重新同步",
  metadata_only: "仅归档邮件元数据与验证码",
};

/**
 * Return a friendly Chinese description for archived mail `content_state`.
 */
export function contentStateLabel(state) {
  const key = String(state ?? "").trim().toLowerCase();
  if (!key) return "正文状态未知";
  return CONTENT_STATE_LABELS[key] || `未知状态（${key}）`;
}

/**
 * Build a complete sandboxed HTML document string for `<iframe sandbox="" :srcdoc="...">`.
 * Never includes any `<script>` tags.
 */
export function buildSandboxedEmailSrcdoc(safeHtml, { dark = false } = {}) {
  const bodyContent = typeof safeHtml === "string" ? safeHtml : "";
  const palette = dark
    ? {
        scheme: "dark",
        bg: "#1d232e",
        text: "#eef2f9",
        muted: "#b4bfd0",
        border: "#333d4c",
        link: "#72b4ff",
        codeBg: "#272f3c",
      }
    : {
        scheme: "light",
        bg: "#ffffff",
        text: "#202631",
        muted: "#586577",
        border: "#e0e5ed",
        link: "#006bdf",
        codeBg: "#f3f5f8",
      };

  return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="color-scheme" content="${palette.scheme}">
<style>
  *, *::before, *::after { box-sizing: border-box; }
  html, body {
    margin: 0;
    padding: 0;
    background: ${palette.bg};
    color: ${palette.text};
    color-scheme: ${palette.scheme};
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", ui-sans-serif, "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
    font-size: 14px;
    line-height: 1.65;
    -webkit-font-smoothing: antialiased;
  }
  body {
    padding: 18px 20px;
    overflow-wrap: anywhere;
    word-break: break-word;
  }
  a {
    color: ${palette.link};
    word-break: break-all;
  }
  img, svg, video, canvas {
    max-width: 100%;
    height: auto;
  }
  table {
    max-width: 100% !important;
    border-collapse: collapse;
  }
  td, th {
    word-break: break-word;
  }
  pre, code {
    font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
    background: ${palette.codeBg};
    border-radius: 6px;
  }
  pre {
    max-width: 100%;
    padding: 12px 14px;
    overflow-x: auto;
    border: 1px solid ${palette.border};
  }
  code {
    padding: 2px 5px;
    font-size: 0.9em;
  }
  pre code {
    padding: 0;
    background: transparent;
  }
  blockquote {
    margin: 12px 0;
    padding: 4px 14px;
    color: ${palette.muted};
    border-left: 3px solid ${palette.border};
  }
  hr {
    border: 0;
    border-top: 1px solid ${palette.border};
    margin: 16px 0;
  }
</style>
</head>
<body>${bodyContent}</body>
</html>`;
}

/**
 * Build a validated same-origin link for `/inbox/view?token=...` share paths.
 */
export function buildInboxShareLink(relativePath, origin = globalThis.location?.origin) {
  if (
    typeof relativePath !== "string" ||
    relativePath !== relativePath.trim() ||
    !relativePath.startsWith("/") ||
    relativePath.startsWith("//")
  ) {
    throw new TypeError("查件分享链接格式无效。");
  }

  let base;
  let target;
  try {
    base = new URL(origin);
    target = new URL(relativePath, `${base.origin}/`);
  } catch {
    throw new TypeError("查件分享链接格式无效。");
  }

  if (
    !["http:", "https:"].includes(base.protocol) ||
    base.username ||
    base.password ||
    target.origin !== base.origin ||
    target.hash
  ) {
    throw new TypeError("查件分享链接格式无效。");
  }

  return target.href;
}

function resolveStorage(explicitStorage) {
  if (explicitStorage !== undefined) return explicitStorage;
  try {
    return typeof globalThis.localStorage !== "undefined" ? globalThis.localStorage : null;
  } catch {
    return null;
  }
}

function normalizeLookupEntry(entry) {
  if (!entry || typeof entry !== "object") return null;
  const parsed = parsePlusTagAddress(entry.address || "");
  const address = parsed.rootAddress || String(entry.address || "").trim().toLowerCase();
  if (!address) return null;

  const tag = String(entry.tag ?? parsed.tag ?? "").trim().toLowerCase();
  const idNum = Number(entry.id);
  return {
    id: Number.isFinite(idNum) && idNum > 0 ? Math.trunc(idNum) : null,
    address,
    label: String(entry.label || "").trim(),
    accountEmail: String(entry.accountEmail || entry.account_email || "").trim(),
    tag,
    viewedAt: String(entry.viewedAt || new Date().toISOString()),
  };
}

/**
 * Load recent inbox lookup history from localStorage (max 8 entries).
 */
export function loadRecentInboxLookups(storage = resolveStorage()) {
  if (!storage || typeof storage.getItem !== "function") {
    return [];
  }
  try {
    const raw = storage.getItem(RECENT_INBOX_LOOKUPS_STORAGE_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed
      .map(normalizeLookupEntry)
      .filter(Boolean)
      .slice(0, MAX_RECENT_INBOX_LOOKUPS);
  } catch {
    return [];
  }
}

/**
 * Save a mailbox lookup entry into localStorage history (deduplicated, max 8).
 */
export function saveRecentInboxLookup(entry, storage = resolveStorage()) {
  const normalized = normalizeLookupEntry(entry);
  const existing = loadRecentInboxLookups(storage);
  if (!normalized) {
    return existing;
  }

  const keyOf = (item) => `${item.address.toLowerCase()}#${(item.tag || "").toLowerCase()}`;
  const targetKey = keyOf(normalized);
  const updated = [
    normalized,
    ...existing.filter((item) => keyOf(item) !== targetKey),
  ].slice(0, MAX_RECENT_INBOX_LOOKUPS);

  if (storage && typeof storage.setItem === "function") {
    try {
      storage.setItem(RECENT_INBOX_LOOKUPS_STORAGE_KEY, JSON.stringify(updated));
    } catch {
      // Storage quota or privacy mode errors are non-fatal.
    }
  }
  return updated;
}

/**
 * Clear recent inbox lookup history from localStorage.
 */
export function clearRecentInboxLookups(storage = resolveStorage()) {
  if (storage && typeof storage.removeItem === "function") {
    try {
      storage.removeItem(RECENT_INBOX_LOOKUPS_STORAGE_KEY);
    } catch {
      // Ignore storage errors.
    }
  }
  return [];
}
