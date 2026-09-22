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

当 issuer 配置为 `https://connect.cs.ac.cn` 时，BYOD Browser 的
`grips://login` 会在普通 profile 中打开 `/browser/login`。中台生成一次性的
state 和 PKCE challenge，再跳转到 discovery 文档中的 authorization endpoint；
考试启动时的 authorization-code + PKCE 跳转会复用该 profile 的 Connect SSO
cookie。中台只在服务端交换 code 并校验 ID token，不读取、复制或下发 Connect
cookie。
生产启动还必须设置非空的 `BYOD_POLICY_SECRET`；只有显式启用 `--dev-auth` 时才
会使用开发密钥。

生产环境的每场考试通过管理后台写入 PostgreSQL。每场考试由服务端生成不可变的 UUID 主键 `id`；管理员填写独立的考试名称 `name` 和用户标签 `hashtag`，后者可修改且不参与内部关联。`base_url` 是考试开始后要打开的完整 HTTPS 页面 URL，例如 `https://cs101.gbu.edu.cn/paper/category/exam`；透明 tunnel 只使用它的 host/port 拨号，路径和查询参数由浏览器打开页面时使用，不做 TLS termination。`--upstream` 仅作为没有数据库记录时的本地开发回退值。

考试状态由后端状态机维护：新建为 `draft`，发布时按开始时间进入 `scheduled` 或 `active`，到达开始/结束时间后由服务端自动推进为 `active`/`ended`。管理端不能直接写入 `state`。

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

访问 `/admin/` 打开控制中心，所有身份均使用 Connect OIDC 登录。权限不是互斥角色：平台管理员由 `platform_admin` 能力授予，可管理全局用户和所有考试；考试管理员通过 `byod_exam_admins` 单独绑定到某场考试，只能管理该考试；普通用户是全局用户目录中的基础身份，也可以同时拥有上述任一能力和考试参加资格。首次部署通过 `BYOD_ADMIN_EMAILS`（Helm 的 `adminEmails`）指定可自动成为平台管理员的已验证邮箱。
后台提供考试、全局用户、考试管理员、考试参加资格、session 和审计日志管理。管理员可以先按邮箱建立用户，再把用户加入考试名单或授予某场考试的管理员能力；只有启用且已通过 OIDC 绑定的用户可以参加，空名单也按拒绝参加处理。用户首次 OIDC 登录后会保存 `nickname` 和 `picture` claim，并在用户目录、考试名单和管理员列表中展示。

生产环境应使用已有 Secret、开启 TLS Ingress，并关闭 `devAuth`；chart 默认的
策略密钥为空，未配置 Secret 的 Pod 会直接退出，避免意外使用公共开发密钥。考试、学生名单、session 和事件存储在 PostgreSQL 中；请设置 `database.existingSecret` 和 `admin.existingSecret`。`migration.enabled` 默认为 true，Deployment 会先运行同版本镜像的 `--migrate` init container，迁移成功后才启动主容器。管理后台位于 `/admin/`，使用 shadcn 风格的响应式控制台；前端 API 客户端由 `openapi.yaml` 自动生成。
`tunnel.endpoint` 必须指向可直通 Pod 8788 的 TCP 地址；`tunnel.service` 仅创建
LoadBalancer/NodePort，不做 TLS termination。若集群使用 Gateway API，请关闭该
Service 并用 TCPRoute 暴露同一个 targetPort。

如果数据中心客户端不能访问公网 DNAT，可同时设置
`tunnel.privateEndpoint` 和逗号分隔的 `tunnel.privateCIDRs`。服务端会根据
`X-Forwarded-For` 的客户端地址，为匹配 CIDR 的配置请求下发内网 endpoint，其他
客户端继续收到 `tunnel.endpoint`；浏览器协议不需要改变。

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

学生考试入口不再使用考试码。管理后台保存考试并配置参加名单后，学生在浏览器
打开裸 `grips://exam/`，完成 Connect OIDC 登录；浏览器调用
`GET /v1/exams/available`，只展示该身份被分配的考试，随后以选中的 `exam_id`
创建作答 session。认证可在开始时间前完成，但 `/start` 直到 `starts_at` 才会成功；
到达 `ends_at` 后在线 session 和 tunnel 都会失效。数据库中的旧 `exam_code` 列仅
为迁移兼容保留，不会返回给浏览器或管理员 API。

获取考试配置：

```bash
curl http://127.0.0.1:8787/course-101/.well-known/byod-configuration
```

配置中的 `policy.document` 是 canonical JSON，`policy.signature` 是使用 `BYOD_POLICY_SECRET` 生成的 HMAC-SHA256。`document.browser` 包含禁止切后台、禁止新标签页、禁止 DevTools、打印/下载/剪贴板等 SEB 风格基线项；`require_fullscreen` 控制进入浏览器窗口全屏，和 `lock_fullscreen` 同时为 `true` 时拦截 Esc、F11 及菜单退出全屏；浏览器必须在进入限制模式前验证签名、`key_id`、考试 ID 和目标 origin。

## 浏览器联调协议

服务启动后可用 `GET /healthz` 或 `GET /readyz` 检查状态；响应中的 `oidc` 字段
表示已配置真实 OIDC 或开发认证适配器。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/browser/login` | 启动浏览器级 Connect authorization-code + PKCE 登录 |
| GET | `/v1/exams/available` | 按当前 OIDC 身份列出被分配的考试 |
| GET | `/{exam_id}/.well-known/byod-configuration` | 读取 OIDC、策略和考试代理信息 |
| POST | `/v1/sessions` | 创建会话，返回登录 URL 和会话 ID |
| GET | `/oidc/callback` | OIDC 回调；服务端交换 code，不把 IdP token 返回浏览器 |
| GET | `/v1/sessions/{id}` | 查询认证和策略状态 |
| POST | `/v1/sessions/{id}/start` | 原子地激活考试会话 |
| POST | `/v1/sessions/{id}/heartbeat` | 更新浏览器存活时间；超过 45 秒未心跳会自动暂停 |
| POST | `/v1/sessions/{id}/end` | 撤销会话，考试结束后解锁 |
| POST | `/v1/exams/{exam_id}/complete` | 正式交卷；写入完成记录并禁止同一学生再次进入 |
| POST | `/v1/sessions/{id}/violations` | 上报切后台、DevTools 等违规；严重违规会将会话置为 `suspended` |
| GET | `/v1/sessions/{id}/events` | 读取本次作答的追加式事件审计记录 |
| POST | `/v1/sessions/{id}/tunnel-ticket` | 为 active session 签发考试窗口内有效的 tunnel ticket；HTTP CONNECT 在 TTL 内可复用，二进制 preface 单次使用；session suspend/end 会立即失效 |
| ANY | `/{exam_id}/{path}` | 旧 HTTP Bearer 代理（仅兼容联调，透明 tunnel 不使用） |

管理员 API（均需 OIDC 管理员 session 和 CSRF token）：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET/POST | `/admin/api/exams` | 列出/创建考试 |
| POST | `/admin/api/exams/{id}/publish` | 发布考试并根据时间窗口设置 scheduled/active |
| GET/PATCH/DELETE | `/admin/api/exams/{id}` | 查看/编辑/删除考试及策略 |
| GET | `/admin/api/sessions` 或 `/admin/api/exams/{id}/sessions` | 查看在线作答 session |
| GET/POST | `/admin/api/sessions/{id}` | 查看或暂停/恢复 session |
| GET | `/admin/api/events` | 查询全局审计事件 |
| GET/POST | `/admin/api/users` | 按邮箱查询/预先建立全局用户 |
| GET/PATCH | `/admin/api/users/{user_id}` | 查看、启停用户和调整 `platform_admin` 能力 |
| GET/PUT/DELETE | `/admin/api/exams/{id}/participants/{user_id}` | 从全局用户目录配置考试参加资格 |
| GET | `/admin/api/exams/{id}/admins` | 查看该考试管理员 |
| PUT/DELETE | `/admin/api/exams/{id}/admins/{user_id}` | 授予/撤销该考试管理员能力（平台管理员） |
| GET | `/admin/api/user-audit` | 查询用户和权限审计日志 |

会话接口使用 `Authorization: Bearer <browser_session_token>`。服务端不会信任浏览器自行提交的用户身份。旧 HTTP Bearer 代理（仅兼容联调）可生成
`X-BYOD-Subject`/`X-BYOD-Session`，透明 BYOD Tunnel 数据面不终止 TLS、也不注入
任何源站 header；它只按服务端签发的 ticket 选择数据库中的源站。

中台对 `Origin: grips://exam` 提供显式 CORS（含 credentials 和预检），其他
Web 页面来源不会被允许调用会话接口。

## 联调流程

1. 浏览器打开 `grips://exam/`，在当前标签页跳转 Connect OIDC 完成登录。
2. 浏览器用登录态读取 `/v1/exams/available`，只展示后台分配给该用户的考试（使用 UUID `id` 作为内部引用，同时展示考试名称和 `hashtag`）；学生不再输入考试码。
3. 学生选择考试后，浏览器用 UUID `exam_id` 调用 `POST /v1/sessions` 创建已认证的作答 session。
4. 考试未开始时落地页倒计时等待；考试开始后必须点击确认按钮，浏览器才请求 `/start`，调用 tunnel-ticket API，并将 `source_url` 页面所在 origin 的 HTTPS 请求通过 L4 tunnel 转发，然后打开配置的完整页面 URL；服务端不会终止或修改源站 TLS。
5. 退出链接对应 `GET /{exam_id}/end` 或 `POST /{exam_id}/complete`；服务端立即撤销代理凭证，浏览器清理本地限制状态。
