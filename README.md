# BYOD 中台参考实现

这是一个 Go 服务，提供考试控制平面、管理员后台和考试源站代理。浏览器与服务端的接口契约位于 `openapi.yaml`，管理员 UI 使用生成的 TypeScript client。

## 启动

```bash
go run ./cmd/byod-server --listen 127.0.0.1:8787 \
  --tunnel-listen 127.0.0.1:8788 \
  --exam-origin https://exam.cs.ac.cn \
  --upstream https://127.0.0.1:9000
```

没有 IdP 配置时可使用开发认证适配器完成浏览器联调（固定 subject 为
`oidc:dev-student-42`，不要用于生产）：

```bash
go run ./cmd/byod-server --dev-auth --listen 127.0.0.1:8787 \
  --tunnel-listen 127.0.0.1:8788 \
  --exam-origin https://exam.cs.ac.cn --upstream https://127.0.0.1:9000
```

生产 OIDC 使用 `--oidc-issuer`、`--oidc-client-id`、`--oidc-client-secret` 和
`--oidc-redirect-url`（也可通过 `BYOD_OIDC_*` 环境变量提供）。
生产启动还必须设置非空的 `BYOD_POLICY_SECRET`；只有显式启用 `--dev-auth` 时才
会使用开发密钥。

生产环境的每场考试源站通过管理后台写入 PostgreSQL 的 `byod_exams.base_url`，不通过环境变量传递。启用透明 tunnel 时，`base_url` 必须是 HTTPS（服务端只拨号到该 origin 的 443/显式端口，不做 TLS termination）；`--upstream` 仅作为没有数据库记录时的本地开发回退值。

每场考试的策略可以通过 `--policy-file` 或 `BYOD_POLICY_FILE` 覆盖，格式参考
[`policy.example.json`](policy.example.json)。中台会强制覆盖 `exam_id` 和
`allowed_origins`，并对最终文档重新签名。

## Helm 部署

```bash
helm lint helm/byod-server
helm upgrade --install byod helm/byod-server \
  --set image.repository=registry.example.com/byod-server \
  --set image.tag=0.1.0 \
  --set examOrigin=https://exam.cs.ac.cn \
  --set upstream=http://exam-upstream:9000 \
  --set tunnel.endpoint=exam-tunnel.cs.ac.cn:443 \
  --set tunnel.service.enabled=true \
  --set policySecret.existingSecret=byod-secrets \
  --set oidc.existingSecret=byod-oidc
```

访问 `/admin/` 打开管理员后台，使用 `X-Admin-Token` 对应的 token 登录。
后台提供考试、源站、策略、学生名单、session 和审计日志管理。

生产环境应使用已有 Secret、开启 TLS Ingress，并关闭 `devAuth`；chart 默认的
策略密钥为空，未配置 Secret 的 Pod 会直接退出，避免意外使用公共开发密钥。考试、学生名单、session 和事件存储在 PostgreSQL 中；请设置 `database.existingSecret` 和 `admin.existingSecret`。`migration.enabled` 默认为 true，Deployment 会先运行同版本镜像的 `--migrate` init container，迁移成功后才启动主容器。管理后台位于 `/admin/`，使用 shadcn 风格的响应式控制台；前端 API 客户端由 `openapi.yaml` 自动生成。
`tunnel.endpoint` 必须指向可直通 Pod 8788 的 TCP 地址；`tunnel.service` 仅创建
LoadBalancer/NodePort，不做 TLS termination。若集群使用 Gateway API，请关闭该
Service 并用 TCPRoute 暴露同一个 targetPort。

## GitHub Actions / GHCR

`release.yml` 会在 `v*` tag 上运行镜像构建和 Helm chart 发布；`ci.yml` 会在每次提交时重新生成并校验 Go/TypeScript 客户端：

```text
ghcr.io/bit-precise/byod-server:<tag>
oci://ghcr.io/bit-precise/charts/byod-server:<chart-version>
```

工作流使用 `GITHUB_TOKEN` 和 `packages: write` 权限。`v0.1.0` 会发布版本
`0.1.0`；普通 `main` 推送使用 `0.0.0-ci.<run-number>` chart 版本。

镜像构建阶段会先执行 `admin-ui` 的 OpenAPI client 生成和 production build，再把
UI 嵌入 Go 二进制；干净 checkout 不依赖本地 `dist` 文件。

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
| POST | `/v1/sessions/{id}/tunnel-ticket` | 为 active session 签发考试窗口内有效的 tunnel ticket；HTTP CONNECT 在 TTL 内可复用，二进制 preface 单次使用；session suspend/end 会立即失效 |
| ANY | `/{exam_id}/{path}` | 旧 HTTP Bearer 代理（仅兼容联调，透明 tunnel 不使用） |

管理员 API（均需 `X-Admin-Token`）：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET/POST | `/admin/api/exams` | 列出/创建考试 |
| GET/PATCH/DELETE | `/admin/api/exams/{id}` | 查看/编辑/删除考试及策略 |
| GET/PUT/DELETE | `/admin/api/exams/{id}/students/{subject}` | 管理考试学生名单 |
| GET | `/admin/api/sessions` 或 `/admin/api/exams/{id}/sessions` | 查看在线作答 session |
| GET/POST | `/admin/api/sessions/{id}` | 查看或暂停/恢复 session |
| GET | `/admin/api/events` | 查询全局审计事件 |

会话接口使用 `Authorization: Bearer <browser_session_token>`。服务端不会信任浏览器自行提交的用户身份。旧 HTTP Bearer 代理（仅兼容联调）可生成
`X-BYOD-Subject`/`X-BYOD-Session`，透明 BYOD Tunnel 数据面不终止 TLS、也不注入
任何源站 header；它只按服务端签发的 ticket 选择数据库中的源站。

中台对 `Origin: grips://exam` 提供显式 CORS（含 credentials 和预检），其他
Web 页面来源不会被允许调用会话接口。

## 联调流程

1. 浏览器把 `https://exam.cs.ac.cn/course-101` 解析为考试 ID，并请求 `.well-known` 配置。
2. 浏览器 `POST /v1/sessions`，在当前标签页打开返回的 `authorization_url`。
3. OIDC 回调落到中台；中台验证 `state`、交换 code，并将会话标记为 `authenticated`。
4. 浏览器轮询会话状态，校验策略签名后 `POST /start`，调用 tunnel-ticket API，并将 `source_origin` 的 HTTPS 请求通过 L4 tunnel 转发；服务端不会终止或修改源站 TLS。
5. 退出链接对应 `GET /{exam_id}/end` 或 `POST /v1/sessions/{id}/end`；服务端立即撤销代理凭证，浏览器清理本地限制状态。
