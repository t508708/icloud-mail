import { buildOTPDirectLink } from "./clipboard.js";

export function buildProjectMailboxFilename(projectName, accountEmail) {
  const safePart = (value, fallback) => (typeof value === "string" ? value : "")
    .replace(/[<>:"/\\|?*\x00-\x1f\x7f]+/g, "-")
    .trim()
    .replace(/\s+/g, "-")
    .replace(/^\.+|\.+$/g, "")
    .slice(0, 80) || fallback;
  return `${safePart(projectName, "project")}-${safePart(accountEmail, "account")}-mailboxes.txt`;
}

export function buildProjectMailboxExportText(mailboxes, origin = globalThis.location?.origin, options = {}) {
  if (!Array.isArray(mailboxes)) {
    throw new TypeError("项目邮箱数据格式无效。");
  }
  const preferProjectToken = Boolean(options?.preferProjectToken);

  return mailboxes.map((mailbox) => {
    const address = mailbox?.address;
    if (typeof address !== "string" || !address.trim() || address !== address.trim() || /[\r\n]/.test(address)) {
      throw new TypeError("邮箱地址格式无效。");
    }
    const rawPath = preferProjectToken && mailbox?.project_otp_url_path
      ? mailbox.project_otp_url_path
      : mailbox?.otp_url_path;
    const link = buildOTPDirectLink(rawPath, origin);
    return `${address}-----${link}`;
  }).join("\r\n");
}

export async function collectProjectMailboxes(fetchPage, pageSize = 50) {
  if (typeof fetchPage !== "function" || !Number.isSafeInteger(pageSize) || pageSize < 1) {
    throw new TypeError("项目邮箱分页参数无效。");
  }

  const items = [];
  for (let pageNumber = 0; pageNumber < 10000; pageNumber += 1) {
    const page = await fetchPage({ limit: pageSize, offset: items.length });
    if (!Array.isArray(page?.items) || !Number.isSafeInteger(page?.total) || page.total < 0 ||
      page.items.length > pageSize || items.length + page.items.length > page.total) {
      throw new TypeError("项目邮箱列表响应格式无效。");
    }
    items.push(...page.items);
    if (items.length === page.total) return items;
    if (page.items.length === 0) break;
  }
  throw new Error("项目邮箱列表读取未完成，请重试。");
}
