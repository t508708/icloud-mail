const MAX_FILTER_LENGTH = 500;
const TOKEN_SPLIT_RE = /[,，;；\r\n]+/;

export const PROJECT_OTP_PRESETS = [
  {
    key: "chatgpt",
    label: "ChatGPT / OpenAI",
    projectSuggested: "ChatGPT",
    senderFilter: "openai.com,tm.openai.com",
    subjectFilter: "OpenAI,ChatGPT,代码,code",
  },
  {
    key: "claude",
    label: "Claude / Anthropic",
    projectSuggested: "Claude",
    senderFilter: "anthropic.com,mail.anthropic.com",
    subjectFilter: "Claude,Anthropic,login,verification",
  },
  {
    key: "deepseek",
    label: "DeepSeek",
    projectSuggested: "DeepSeek",
    senderFilter: "deepseek.com",
    subjectFilter: "DeepSeek,验证码,code",
  },
  {
    key: "cursor",
    label: "Cursor",
    projectSuggested: "Cursor",
    senderFilter: "cursor.sh,cursor.com,workos.com",
    subjectFilter: "Cursor,verification,code",
  },
  {
    key: "google",
    label: "Google",
    projectSuggested: "Google",
    senderFilter: "google.com,accounts.google.com",
    subjectFilter: "Google,验证码,Verification",
  },
  {
    key: "github",
    label: "GitHub",
    projectSuggested: "GitHub",
    senderFilter: "github.com",
    subjectFilter: "GitHub,verification,launch code",
  },
  {
    key: "microsoft",
    label: "Microsoft",
    projectSuggested: "Microsoft",
    senderFilter: "microsoft.com,accountprotection.microsoft.com",
    subjectFilter: "Microsoft,安全代码,security code",
  },
  {
    key: "apple",
    label: "Apple",
    projectSuggested: "Apple",
    senderFilter: "apple.com,id.apple.com",
    subjectFilter: "Apple,验证码,verification",
  },
  {
    key: "discord",
    label: "Discord",
    projectSuggested: "Discord",
    senderFilter: "discord.com",
    subjectFilter: "Discord",
  },
  {
    key: "telegram",
    label: "Telegram",
    projectSuggested: "Telegram",
    senderFilter: "telegram.org",
    subjectFilter: "Telegram,code",
  },
  {
    key: "x",
    label: "X / Twitter",
    projectSuggested: "X",
    senderFilter: "x.com,twitter.com",
    subjectFilter: "confirmation code,确认码,X",
  },
  {
    key: "tiktok",
    label: "TikTok",
    projectSuggested: "TikTok",
    senderFilter: "tiktok.com",
    subjectFilter: "TikTok,verification,验证码",
  },
];

function extractUniqueTokens(input, { lowercase = true } = {}) {
  const raw = Array.isArray(input) ? input.join(",") : String(input ?? "");
  if (!raw.trim()) {
    return [];
  }

  const seen = new Set();
  const tokens = [];
  for (const part of raw.split(TOKEN_SPLIT_RE)) {
    const trimmed = part.trim();
    if (!trimmed) {
      continue;
    }
    const normalized = trimmed.toLowerCase();
    if (seen.has(normalized)) {
      continue;
    }
    seen.add(normalized);
    tokens.push(lowercase ? normalized : trimmed);
  }
  return tokens;
}

/**
 * 将逗号、换行或分号分隔的过滤词规范化为去重、去空、小写的逗号分隔字符串（最长 500 字符）。
 */
export function normalizeFilterTokens(input) {
  const tokens = extractUniqueTokens(input, { lowercase: true });
  if (tokens.length === 0) {
    return "";
  }

  let result = "";
  for (const token of tokens) {
    if (!result) {
      if (token.length > MAX_FILTER_LENGTH) {
        return token.slice(0, MAX_FILTER_LENGTH);
      }
      result = token;
      continue;
    }
    const candidate = `${result},${token}`;
    if (candidate.length > MAX_FILTER_LENGTH) {
      break;
    }
    result = candidate;
  }
  return result;
}

/**
 * 根据项目的发件人与主题过滤规则返回人类可读摘要。
 */
export function describeProjectRule(client) {
  const senderRaw = client?.sender_filter || client?.senderFilter || "";
  const subjectRaw = client?.subject_filter || client?.subjectFilter || "";
  const senderTokens = extractUniqueTokens(senderRaw, { lowercase: false });
  const subjectTokens = extractUniqueTokens(subjectRaw, { lowercase: false });

  if (senderTokens.length === 0 && subjectTokens.length === 0) {
    return "不限来源（接收该邮箱全部验证码）";
  }
  if (senderTokens.length > 0 && subjectTokens.length > 0) {
    return `发件人包含 ${senderTokens.join("、")}，且主题包含 ${subjectTokens.join("、")}`;
  }
  if (senderTokens.length > 0) {
    return `仅限发件人包含 ${senderTokens.join("、")}`;
  }
  return `仅限主题包含 ${subjectTokens.join("、")}`;
}
