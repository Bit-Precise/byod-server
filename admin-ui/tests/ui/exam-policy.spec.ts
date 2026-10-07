import { test, expect, type Page } from "@playwright/test";

const examID = "2fad0191-a522-4f23-8607-1a1717ebdbdd";
const initialPolicy = {
  browser: { require_fullscreen: true, lock_fullscreen: true, vendor_extension: "keep" },
  navigation: { allowed_origins: ["https://iaaa.gbu.edu.cn"], extension: { keep: true } },
  tunnel_hosts: ["cs101.gbu.edu.cn"], session: { heartbeat_seconds: 15, max_idle_seconds: 45 },
  extension: { data: [1, 2, 3] },
};

async function mockAPI(page: Page, policy: unknown = initialPolicy) {
  const saved: any[] = [];
  const exam = { id: examID, name: "策略回归测试", hashtag: "policy-test", base_url: "https://cs101.gbu.edu.cn/paper/category/exam", state: "active", policy };
  await page.route("**/auth/me", (route) => route.fulfill({ json: { user: { id: "test-admin", display_name: "管理员", platform_admin: true, enabled: true }, capabilities: { platform_admin: true, exam_admin: true }, csrf_token: "test-csrf" } }));
  await page.route("**/admin/api/**", async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    if (request.method() === "POST" || request.method() === "PATCH") {
      const body = request.postDataJSON();
      saved.push(body);
      Object.assign(exam, body);
      return route.fulfill({ json: exam });
    }
    return route.fulfill({ json: pathname === "/admin/api/exams" ? [exam] : [], headers: { "X-Total-Count": "0" } });
  });
  return { saved, exam };
}

async function choose(page: Page, label: string, choice: string) {
  await page.getByRole("combobox", { name: label, exact: true }).click();
  await page.getByRole("option", { name: choice, exact: true }).click();
}

test("edit and reopen without JSON; preserves extensions and independent routing lists", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const { saved } = await mockAPI(page);
  await page.goto(`/admin/exams/${examID}/edit`);
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.locator("textarea")).toHaveCount(0);
  await expect(page.getByRole("combobox", { name: "进入考试后自动全屏", exact: true })).toContainText("开启");
  await page.screenshot({ path: test.info().outputPath("policy-editor-desktop.png") });
  await page.getByRole("button", { name: "添加网站", exact: true }).click();
  await page.getByRole("textbox", { name: "额外允许访问的网站 2", exact: true }).fill("https://bridge.cs101.gbu.edu.cn/");
  await page.getByRole("button", { name: "添加主机", exact: true }).click();
  await page.getByRole("textbox", { name: "Tunnel hosts 2", exact: true }).fill("minio.cs101.gbu.edu.cn");
  await choose(page, "进入考试后自动全屏", "关闭");
  await expect(page.getByRole("combobox", { name: "考试期间禁止退出全屏", exact: true })).toContainText("关闭");
  await choose(page, "考试期间禁止退出全屏", "开启");
  await expect(page.getByRole("combobox", { name: "进入考试后自动全屏", exact: true })).toContainText("开启");
  await page.getByRole("button", { name: "保存考试", exact: true }).click();
  await expect.poll(() => saved.length).toBe(1);
  expect(saved[0].policy).toEqual({ ...initialPolicy, navigation: { ...initialPolicy.navigation, allowed_origins: ["https://iaaa.gbu.edu.cn", "https://bridge.cs101.gbu.edu.cn"] }, tunnel_hosts: ["cs101.gbu.edu.cn", "minio.cs101.gbu.edu.cn"] });
  await page.goto(`/admin/exams/${examID}/edit`);
  await expect(page.getByRole("textbox", { name: "额外允许访问的网站 2", exact: true })).toHaveValue("https://bridge.cs101.gbu.edu.cn");
  expect(errors).toEqual([]);
});

test("new exam keeps inherited defaults and does not implicitly tunnel its source", async ({ page }) => {
  const { saved } = await mockAPI(page);
  await page.goto("/admin/exams/new");
  await page.getByLabel("考试名称", { exact: true }).fill("新考试");
  await page.getByLabel("考试 hashtag", { exact: true }).fill("new-test");
  await page.getByLabel("源站 Base URL", { exact: true }).fill("https://cs101.gbu.edu.cn/paper/category/exam");
  await page.getByRole("button", { name: "保存考试", exact: true }).click();
  await expect.poll(() => saved.length).toBe(1);
  expect(saved[0].policy).toEqual({});
});

test("configurable session expiry uses 300 seconds by default and saves a custom deadline", async ({ page }) => {
  const { saved } = await mockAPI(page, {});
  await page.goto(`/admin/exams/${examID}/edit`);
  await page.getByText("会话与心跳", { exact: true }).click();
  const timeout = page.getByLabel("无心跳自动结束（秒）", { exact: true });
  await expect(timeout).toHaveAttribute("placeholder", "继承（内置 300）");
  await expect(page.getByText(/默认 300 秒（5 分钟）/)).toBeVisible();
  await timeout.fill("600");
  await page.getByRole("button", { name: "保存考试", exact: true }).click();
  await expect.poll(() => saved.length).toBe(1);
  expect(saved[0].policy.session.max_idle_seconds).toBe(600);
  await page.goto(`/admin/exams/${examID}/edit`);
  await page.getByText("会话与心跳", { exact: true }).click();
  await expect(timeout).toHaveValue("600");
  await page.getByRole("button", { name: "无心跳自动结束（秒）：继承服务端配置", exact: true }).click();
  await page.getByRole("button", { name: "保存考试", exact: true }).click();
  await expect.poll(() => saved.length).toBe(2);
  expect(saved[1].policy.session?.max_idle_seconds).toBeUndefined();
});

test("rejects invalid hosts, origin paths and inconsistent heartbeat values without sending a save", async ({ page }) => {
  const { saved } = await mockAPI(page);
  await page.goto(`/admin/exams/${examID}/edit`);
  await page.getByRole("textbox", { name: "额外允许访问的网站 1", exact: true }).fill("https://iaaa.gbu.edu.cn/login");
  await page.getByRole("textbox", { name: "Tunnel hosts 1", exact: true }).fill("https://cs101.gbu.edu.cn");
  await page.getByText("会话与心跳", { exact: true }).click();
  await page.getByLabel("心跳间隔（秒）", { exact: true }).fill("60");
  await page.getByLabel("无心跳自动结束（秒）", { exact: true }).fill("45");
  await page.getByRole("button", { name: "保存考试", exact: true }).click();
  await expect(page.getByText("请修正浏览器策略中标出的配置项后再保存。")).toBeVisible();
  await expect(page.getByText("最大空闲时间不能小于心跳间隔。", { exact: true })).toBeVisible();
  expect(saved).toEqual([]);
  await page.getByRole("textbox", { name: "额外允许访问的网站 1", exact: true }).fill("https://iaaa.gbu.edu.cn");
  await page.getByRole("textbox", { name: "Tunnel hosts 1", exact: true }).fill("cs101.gbu.edu.cn");
  await page.getByLabel("无心跳自动结束（秒）", { exact: true }).fill("120");
  await page.getByRole("button", { name: "保存考试", exact: true }).click();
  await expect.poll(() => saved.length).toBe(1);
  expect(saved[0].policy.session).toEqual({ heartbeat_seconds: 60, max_idle_seconds: 120 });
});

test("explicit empty list differs from reset to inherited configuration", async ({ page }) => {
  const { saved } = await mockAPI(page);
  await page.goto(`/admin/exams/${examID}/edit`);
  await page.getByRole("button", { name: "删除Tunnel hosts 1", exact: true }).click();
  await page.getByRole("button", { name: "保存考试", exact: true }).click();
  await expect.poll(() => saved.length).toBe(1);
  expect(saved[0].policy.tunnel_hosts).toEqual([]);
  await page.goto(`/admin/exams/${examID}/edit`);
  await page.getByRole("button", { name: "Tunnel hosts：继承服务端配置", exact: true }).click();
  await page.getByRole("button", { name: "保存考试", exact: true }).click();
  await expect.poll(() => saved.length).toBe(2);
  expect(saved[1].policy).not.toHaveProperty("tunnel_hosts");
});

test("mobile editor has no horizontal overflow and labels reserved controls honestly", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await mockAPI(page);
  await page.goto(`/admin/exams/${examID}/edit`);
  await page.getByText("其他策略与兼容设置", { exact: true }).click();
  await expect(page.getByText(/当前版本尚未按这些字段执行/)).toBeVisible();
  await choose(page, "允许下载", "开启");
  expect(await page.getByRole("dialog").evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
  await page.screenshot({ path: test.info().outputPath("policy-editor-mobile.png") });
});

test("multi-line paste and cancel do not lose the saved policy", async ({ page }) => {
  const { saved } = await mockAPI(page);
  await page.goto(`/admin/exams/${examID}/edit`);
  await page.getByRole("button", { name: "添加主机", exact: true }).click();
  await page.getByRole("textbox", { name: "Tunnel hosts 2", exact: true }).evaluate((el) => {
    const data = new DataTransfer();
    data.setData("text/plain", "minio.cs101.gbu.edu.cn\nbridge.cs101.gbu.edu.cn");
    el.dispatchEvent(new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true }));
  });
  await expect(page.getByRole("textbox", { name: "Tunnel hosts 3", exact: true })).toHaveValue("bridge.cs101.gbu.edu.cn");
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
  expect(saved).toEqual([]);
  await page.goto(`/admin/exams/${examID}/edit`);
  await expect(page.getByRole("textbox", { name: "Tunnel hosts 2", exact: true })).toHaveCount(0);
  await expect(page.getByRole("textbox", { name: "Tunnel hosts 1", exact: true })).toHaveValue("cs101.gbu.edu.cn");
});

test("malformed legacy configuration is repairable without a JSON fallback", async ({ page }) => {
  const { saved } = await mockAPI(page, { browser: false, navigation: { allowed_origins: "invalid", extension: "keep" }, tunnel_hosts: [false] });
  await page.goto(`/admin/exams/${examID}/edit`);
  await page.getByRole("button", { name: "保存考试", exact: true }).click();
  await expect(page.getByText("请修正浏览器策略中标出的配置项后再保存。")).toBeVisible();
  expect(saved).toEqual([]);
  await page.getByRole("button", { name: "恢复 browser 默认配置", exact: true }).click();
  await page.getByRole("button", { name: "额外允许访问的网站：使用空列表", exact: true }).click();
  await page.getByRole("button", { name: "Tunnel hosts：使用空列表", exact: true }).click();
  await page.getByRole("button", { name: "保存考试", exact: true }).click();
  await expect.poll(() => saved.length).toBe(1);
  expect(saved[0].policy).toEqual({ navigation: { allowed_origins: [], extension: "keep" }, tunnel_hosts: [] });
});
