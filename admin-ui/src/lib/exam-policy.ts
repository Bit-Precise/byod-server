export type ExamPolicy = Record<string, unknown>;

export type PolicyField = {
  path: string;
  label: string;
  description?: string;
  kind: "boolean" | "hosts" | "origins" | "paths" | "schemes" | "integer" | "action";
  fallback?: boolean | number | string;
  min?: number;
  max?: number;
  reserved?: boolean;
};

export const fullscreenFields: PolicyField[] = [
  { path: "browser.require_fullscreen", label: "进入考试后自动全屏", kind: "boolean", fallback: false, description: "考试开始后将整个浏览器窗口切换为全屏。" },
  { path: "browser.lock_fullscreen", label: "考试期间禁止退出全屏", kind: "boolean", fallback: false, description: "需同时开启自动全屏；拦截 Esc、F11 和菜单中的退出全屏操作。" },
];

export const networkFields: PolicyField[] = [
  { path: "navigation.allowed_origins", label: "额外允许访问的网站", kind: "origins", description: "填写完整 HTTP(S) origin，例如 https://iaaa.gbu.edu.cn。不能带页面路径、查询参数或凭据。考试入口和源站自动允许；此列表不决定是否走 tunnel。" },
  { path: "tunnel_hosts", label: "Tunnel hosts", kind: "hosts", description: "填写精确域名，例如 cs101.gbu.edu.cn、minio.cs101.gbu.edu.cn，不带协议、端口或路径。最多 64 个。仅这里列出的 HTTPS 主机走 tunnel，其他请求直连；源站不会自动加入。" },
];

export const sessionFields: PolicyField[] = [
  { path: "session.heartbeat_seconds", label: "心跳间隔（秒）", kind: "integer", fallback: 15, min: 5, max: 300, description: "考试入口页发送心跳的间隔，范围 5–300 秒；源站打开后，活跃 tunnel 也会维持心跳。" },
  { path: "session.max_idle_seconds", label: "最大空闲时间（秒）", kind: "integer", fallback: 45, min: 5, max: 3600, description: "无心跳时触发暂停，范围 5–3600 秒，不能小于心跳间隔。这不是 EdgeOne 或代理连接超时。" },
];

// These names are part of the signed document but are not wired into the
// current browser's runtime enforcement. Keep them editable without claiming
// that changing them enables capabilities in an already-built browser.
export const reservedBrowserFields: PolicyField[] = [
  ["allow_background", "允许切换到后台", false],
  ["allow_new_tabs", "允许新建标签页", false],
  ["allow_new_windows", "允许新建窗口", false],
  ["allow_devtools", "允许开发者工具", false],
  ["allow_print", "允许打印", false],
  ["allow_view_source", "允许查看网页源代码", false],
  ["allow_save_page", "允许保存网页", false],
  ["allow_downloads", "允许下载", false],
  ["allow_extensions", "允许扩展程序", false],
  ["allow_incognito", "允许无痕模式", false],
  ["allow_fullscreen", "允许网页请求全屏", true],
  ["allow_clipboard_read", "允许读取剪贴板", false],
  ["allow_clipboard_write", "允许写入剪贴板", false],
  ["allow_screen_capture", "允许屏幕捕获", false],
  ["allow_navigation_outside_exam", "允许访问考试范围外的网站", false],
  ["kiosk_mode", "启用考试专用模式", true],
  ["exit_requires_unlock", "退出需解锁", true],
].map(([key, label, fallback]) => ({ path: `browser.${key}`, label: String(label), kind: "boolean", fallback: Boolean(fallback), reserved: true }));

export const advancedFields: PolicyField[] = [
  { path: "navigation.blocked_schemes", label: "禁止的 URL 协议", kind: "schemes", reserved: true, description: "填写协议名称，例如 file、javascript、data、devtools。当前属于预留策略，不替代原生浏览器的安全限制。" },
  { path: "navigation.allowed_paths", label: "控制面代理允许的路径", kind: "paths", description: "仅约束旧 HTTP 路径代理，例如 /course-101/**。不会检查端到端 TLS 中的源站路径；继承时由服务端按考试 ID 生成。" },
  { path: "allowed_paths", label: "旧版顶层路径规则", kind: "paths", description: "保留旧考试的路径配置；navigation 中的路径规则优先。" },
];

export const violationFields: PolicyField[] = [
  { path: "violations.on_background", label: "切换后台时", kind: "action", fallback: "suspend", reserved: true },
  { path: "violations.on_new_tab", label: "新建标签页时", kind: "action", fallback: "block", reserved: true },
  { path: "violations.on_devtools", label: "打开开发者工具时", kind: "action", fallback: "block", reserved: true },
  { path: "violations.max_background_events", label: "允许切换后台的次数", kind: "integer", fallback: 0, min: 0, max: 1000000, reserved: true },
];

export const policyFields = [...fullscreenFields, ...networkFields, ...sessionFields, ...reservedBrowserFields, ...advancedFields, ...violationFields];
export const actionOptions = [{ value: "block", label: "阻止" }, { value: "suspend", label: "暂停考试" }, { value: "report", label: "仅记录" }];

export function isPolicyObject(value: unknown): value is ExamPolicy {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function policyValue(policy: ExamPolicy, path: string): unknown {
  const [section, key] = path.split(".");
  return key ? (isPolicyObject(policy[section]) ? policy[section][key] : undefined) : policy[section];
}

// Undefined means inherit (remove this override), not false or an empty list.
// Clone only the edited branch; unknown extension fields survive every edit.
export function setPolicyValue(policy: ExamPolicy, path: string, value: unknown): ExamPolicy {
  const result = { ...policy };
  const [section, key] = path.split(".");
  if (!key) {
    if (value === undefined) delete result[section];
    else result[section] = value;
    return result;
  }
  const group = { ...(isPolicyObject(policy[section]) ? policy[section] : {}) };
  if (value === undefined) delete group[key];
  else group[key] = value;
  if (Object.keys(group).length) result[section] = group;
  else delete result[section];
  return result;
}

export function setFullscreenValue(policy: ExamPolicy, path: string, value: unknown): ExamPolicy {
  let result = setPolicyValue(policy, path, value);
  if (path === "browser.lock_fullscreen" && value === true) {
    result = setPolicyValue(result, "browser.require_fullscreen", true);
  } else if (path === "browser.require_fullscreen" && value === false && policyValue(result, "browser.lock_fullscreen") === true) {
    result = setPolicyValue(result, "browser.lock_fullscreen", false);
  }
  return result;
}

export function normalizePolicyList(kind: PolicyField["kind"], entries: string[]): string[] {
  const items = entries.map((entry) => {
    const trimmed = entry.trim();
    if (kind === "hosts" || kind === "schemes") return trimmed.toLowerCase();
    if (kind === "origins") {
      const url = new URL(trimmed);
      if (!/^https?:\/\//i.test(trimmed) || /[\s?#\\]/.test(trimmed) ||
          /\/\/[^/]*@/.test(trimmed) || !["http:", "https:"].includes(url.protocol) ||
          url.pathname !== "/" || url.username || url.password) throw new Error("网站必须是 HTTP(S) origin，不能带路径、查询参数、片段或凭据。");
      return url.origin;
    }
    return trimmed;
  });
  return [...new Set(items)];
}

export function validatePolicy(policy: ExamPolicy): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const section of ["browser", "navigation", "session", "violations"]) {
    if (policy[section] !== undefined && !isPolicyObject(policy[section])) {
      errors[section] = "旧配置格式不正确，请恢复此组的服务端默认配置后重新设置。";
    }
  }
  for (const field of policyFields) {
    const value = policyValue(policy, field.path);
    if (value === undefined) continue;
    if (field.kind === "boolean") {
      if (typeof value !== "boolean") errors[field.path] = "请选择开启、关闭或继承配置。";
    } else if (field.kind === "integer") {
      if (typeof value !== "number" || !Number.isSafeInteger(value) || value < field.min! || value > field.max!) {
        errors[field.path] = `请输入 ${field.min}–${field.max} 范围内的整数。`;
      }
    } else if (field.kind === "action") {
      // Unknown historical actions are preserved and shown as a custom
      // selection; this reserved field must not force unrelated data loss.
      if (typeof value !== "string" || !value.trim()) errors[field.path] = "请选择处理方式或继承配置。";
    } else {
      if (field.kind === "hosts" && value === null) continue; // Existing server semantics: no tunnel.
      if (!Array.isArray(value) || !value.every((item) => typeof item === "string")) {
        errors[field.path] = "旧列表格式不正确，请重置列表后重新添加。";
        continue;
      }
      const entries = value.filter((entry: string) => entry.trim());
      try {
        const normalized = normalizePolicyList(field.kind, entries);
        if (field.kind === "hosts") {
          if (normalized.length > 64) throw new Error("Tunnel hosts 最多允许 64 个主机。");
          for (const host of normalized) {
            if (host.length > 253 || !host.split(".").every((label) => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label)) ||
                new URL(`https://${host}`).hostname !== host) {
              throw new Error(`“${host}”不是有效的精确主机名；不能带协议、端口、路径或通配符。`);
            }
          }
        } else if (field.kind === "schemes" && normalized.some((item) => !/^[a-z][a-z0-9+.-]*$/.test(item))) {
          throw new Error("协议名称不包含冒号或路径，例如 file、javascript。");
        } else if (field.kind === "paths" && normalized.some((item) => !item.startsWith("/") || /[?#\s]/.test(item) || (item.includes("*") && (!item.endsWith("/**") || item.slice(0, -3).includes("*"))))) {
          throw new Error("请输入以 / 开头的精确路径，或以 /** 结尾的路径前缀。");
        }
      } catch (error) {
        errors[field.path] = error instanceof TypeError ? "地址格式不正确，请检查协议、域名和端口。" : (error as Error).message;
      }
    }
  }
  const heartbeat = policyValue(policy, "session.heartbeat_seconds");
  const idle = policyValue(policy, "session.max_idle_seconds");
  if (typeof heartbeat === "number" && typeof idle === "number" && idle < heartbeat) errors["session.max_idle_seconds"] = "最大空闲时间不能小于心跳间隔。";
  if (policyValue(policy, "browser.lock_fullscreen") === true && policyValue(policy, "browser.require_fullscreen") === false) errors["browser.lock_fullscreen"] = "禁止退出全屏需要同时开启自动全屏。";
  return errors;
}

export function serializePolicy(policy: ExamPolicy): ExamPolicy {
  const errors = validatePolicy(policy);
  if (Object.keys(errors).length) throw new Error(Object.values(errors)[0]);
  let result = policy;
  for (const field of policyFields) {
    const value = policyValue(result, field.path);
    if (Array.isArray(value)) result = setPolicyValue(result, field.path, normalizePolicyList(field.kind, value.filter((entry: string) => entry.trim())));
  }
  return result;
}
