# BYOD 中台参考实现

这是一个 Go 联调服务，固定了浏览器和考试后端之间的协议。它适合本地联调和接口测试，生产部署时应把策略密钥、会话存储和反向代理替换成正式组件。

## 启动

```bash
go run ./cmd/byod-middleware --listen 127.0.0.1:8787 \
  --exam-origin https://exam.cs.ac.cn \
  --upstream http://127.0.0.1:9000
```

没有 IdP 配置时可使用开发认证适配器完成浏览器联调（固定 subject 为
`oidc:dev-student-42`，不要用于生产）：

```bash
go run ./cmd/byod-middleware --dev-auth --listen 127.0.0.1:8787 \
  --exam-origin https://exam.cs.ac.cn --upstream http://127.0.0.1:9000
```

生产 OIDC 使用 `--oidc-issuer`、`--oidc-client-id`、`--oidc-client-secret` 和
`--oidc-redirect-url`（也可通过 `BYOD_OIDC_*` 环境变量提供）。
生产启动还必须设置非空的 `BYOD_POLICY_SECRET`；只有显式启用 `--dev-auth` 时才
会使用开发密钥。

每场考试的策略可以通过 `--policy-file` 或 `BYOD_POLICY_FILE` 覆盖，格式参考
[`policy.example.json`](policy.example.json)。中台会强制覆盖 `exam_id` 和
`allowed_origins`，并对最终文档重新签名。

## Helm 部署

```bash
helm lint helm/byod-middleware
helm upgrade --install byod helm/byod-middleware \
  --set image.repository=registry.example.com/byod-middleware \
  --set image.tag=0.1.0 \
  --set examOrigin=https://exam.cs.ac.cn \
  --set upstream=http://exam-upstream:9000 \
  --set policySecret.existingSecret=byod-secrets \
  --set oidc.existingSecret=byod-oidc
```

生产环境应使用已有 Secret、开启 TLS Ingress，并关闭 `devAuth`；chart 默认的
策略密钥为空，未配置 Secret 的 Pod 会直接退出，避免意外使用公共开发密钥。当前会话和事件存储在进程内存中，因此 chart
默认单副本；接入 Redis/PostgreSQL 共享存储后才能安全扩展到多副本。

## GitHub Actions / GHCR

`release.yml` 会在 `main` 推送和 `v*` tag 上运行测试、镜像构建和 Helm chart 发布：

```text
ghcr.io/bit-precise/byod-server:<tag>
oci://ghcr.io/bit-precise/charts/byod-middleware:<chart-version>
```

工作流使用 `GITHUB_TOKEN` 和 `packages: write` 权限。`v0.1.0` 会发布版本
`0.1.0`；普通 `main` 推送使用 `0.0.0-ci.<run-number>` chart 版本。

获取考试配置：

```bash
curl http://127.0.0.1:8787/course-101/.well-known/byod-configuration
```

配置中的 `policy.document` 是 canonical JSON，`policy.signature` 是使用 `BYOD_POLICY_SECRET` 生成的 HMAC-SHA256。`document.browser` 包含禁止切后台、禁止新标签页、禁止 DevTools、打印/下载/剪贴板等 SEB 风格基线项；浏览器必须在进入限制模式前验证签名、`key_id`、考试 ID 和目标 origin。

## 浏览器联调协议

服务启动后可用 `GET /healthz` 或 `GET /readyz` 检查状态；响应中的 `oidc` 字段
表示已配置真实 OIDC 或开发认证适配器。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/{exam_id}/.well-known/byod-configuration` | 读取 OIDC、策略和考试代理信息 |
| POST | `/v1/sessions` | 创建会话，返回登录 URL 和会话 ID |
| GET | `/oidc/callback` | OIDC 回调；服务端交换 code，不把 IdP token 返回浏览器 |
| GET | `/v1/sessions/{id}` | 查询认证和策略状态 |
| POST | `/v1/sessions/{id}/start` | 原子地激活考试会话 |
| POST | `/v1/sessions/{id}/heartbeat` | 更新浏览器存活时间；超过 45 秒未心跳会自动暂停 |
| POST | `/v1/sessions/{id}/end` | 撤销会话，考试结束后解锁 |
| POST | `/v1/sessions/{id}/violations` | 上报切后台、DevTools 等违规；严重违规会将会话置为 `suspended` |
| GET | `/v1/sessions/{id}/events` | 读取本次作答的追加式事件审计记录 |
| ANY | `/{exam_id}/{path}` | 回源并注入身份头 |

会话接口使用 `Authorization: Bearer <browser_session_token>`。服务端不会信任浏览器自行提交的用户身份；`X-BYOD-Subject` 和 `X-BYOD-Session` 只由代理根据服务端会话生成。代理只允许访问启动参数指定的 upstream，避免形成开放代理。

中台对 `Origin: grips://exam` 提供显式 CORS（含 credentials 和预检），其他
Web 页面来源不会被允许调用会话接口。

## 联调流程

1. 浏览器把 `https://exam.cs.ac.cn/course-101` 解析为考试 ID，并请求 `.well-known` 配置。
2. 浏览器 `POST /v1/sessions`，在当前标签页打开返回的 `authorization_url`。
3. OIDC 回调落到中台；中台验证 `state`、交换 code，并将会话标记为 `authenticated`。
4. 浏览器轮询会话状态，校验策略签名后 `POST /start`，再把题目请求指向 `proxy_origin`。
5. 退出链接对应 `GET /{exam_id}/end` 或 `POST /v1/sessions/{id}/end`；服务端立即撤销代理凭证，浏览器清理本地限制状态。
