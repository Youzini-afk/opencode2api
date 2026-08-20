# opencode2api

`opencode2api` 是一个使用 Go 编写的 OpenCode Zen / Zen Go 协议代理。它对外提供标准 OpenAI 与 Anthropic API，并自动添加 OpenCode 客户端请求头。

主要功能：

- 支持 OpenAI Chat Completions、Responses 和 Models API
- 支持 Anthropic Messages API
- 支持普通响应和 SSE 流式响应
- 支持文本、图片、thinking/reasoning、工具定义、工具调用和工具结果转换
- 分离配置 Zen key 池与 Zen Go key 池；没有 Zen key 时自动使用匿名 Zen
- 模型同时存在于两个上游时按 `prefer` 配置优先使用 Go 或 Zen（默认 Go）
- 可显式启用匿名优先；匿名请求只有遇到 `429` 才回退到首选 key，`401/403` 直接返回
- 每 24 小时从 models.dev 更新成本与弃用信息，仅用于 WebUI 诊断，不参与模型隐藏或路由许可
- 支持直连、HTTP、HTTPS、SOCKS5 和 SOCKS5H 代理
- 支持从文本文件读取代理池，并与配置内的代理合并、去重
- `config.json` 支持 `//` 和 `/* ... */` 注释
- 将 key 自动均衡绑定到代理，保持连接亲和性
- 使用稳定会话哈希保持同一会话的 key/proxy 亲和性，并在节点故障时自动回退
- 代理失败后自动迁移绑定，key 失败后进行短时冷却
- 根据真实上游流量识别代理故障，并每 15 分钟通过 Cloudflare trace 并行复查异常代理
- 为不同会话生成不同的 OpenCode 会话 ID，并支持 `x-opencode-session`、`x-session-id` 和 `conversation-id` 显式指定会话
- 内置 Field Manual WebUI，可与 API 共用一个端口或独立监听，并提供配置、Token/上游指标、路由诊断、三协议 Playground 与实时日志
- WebUI 使用账号密码、服务端 session、HttpOnly Cookie、CSRF 与登录限速保护
- WebUI 保存后原子写入配置并热切换 Gateway；无效配置不会影响当前流量
- stdout 输出结构化 JSON 日志；请求、Token、上游尝试与最近一小时滚动指标仅保存在进程内存中

## API 路径

| 方法 | 路径 | 协议 |
| --- | --- | --- |
| `GET` | `/v1/models` | OpenAI 模型列表 |
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions |
| `POST` | `/v1/responses` | OpenAI Responses |
| `POST` | `/v1/messages` | Anthropic Messages |
| `GET` | `/healthz` | 健康检查 |

`/healthz` 无需 API key，返回服务版本以及模型目录、Zen/Go key、匿名开关和代理池的汇总状态，不会暴露 key 或代理地址。模型目录尚未完成首次刷新、已经过期、没有可暴露模型或没有健康代理时返回 HTTP `503`；其余情况返回 `200`。

模型目录的过期阈值为 `models.refresh_seconds` 的两倍，且不低于 60 秒。刚启动时短暂返回 `503 starting` 属于正常现象，模型列表首次刷新成功后会变为 `200 ok`。

健康检查不会计入请求指标。它也不会触发 models.dev 刷新或写入任何监控记录。

## WebUI

直接运行二进制并使用示例配置时，管理界面位于独立的 `8081` 端口：

```text
http://服务器地址:8081
```

首次账号为 `admin`，密码来自 `webui.password`。服务第一次成功启动时会使用 Argon2id 将密码转换为带盐哈希，写入 `webui.password_hash`，并从配置中删除明文密码。请在首次登录后立即修改示例密码。

Field Manual WebUI 包含运行桌面、六步首次运行检查、接入手册、Token 用量、三协议 Playground、路由诊断、配置、事件日志和账号安全页面。接入手册会生成 Chat、Responses、Anthropic、Python 和 JavaScript 示例，但始终使用 `YOUR_API_KEY` 占位符，不把真实 Server Key 写入页面。

Token 页面展示用量覆盖率、每分钟趋势、模型排行与 Zen/Go Tier 分布。诊断页展示 models.dev 状态与成本参考、模型原生协议、实际上游决定的匿名语义、Key/代理状态、逐次上游尝试以及最近一次 Playground 追踪；实时日志通过 SSE 推送。所有动态管理数据都以 DOM 文本节点渲染。

监控、上游尝试、最近 Playground 结果和日志仅保存在内存。**进程重启会清空全部监控与诊断历史，包括 lifetime 累计。** stdout 日志仍可由 Docker 或日志平台收集；models.dev 价格快取是独立的磁盘兼容资料，不属于监控历史。

### 监控字段

登录 WebUI 后，`GET /api/monitor` 返回以下顶层字段；该管理 API 只在 `webui.listen` 上提供，并受 Session 保护：

| 字段 | 内容 |
| --- | --- |
| `version` | 当前程序版本。 |
| `metrics` | 原有请求统计：进程启动时间、活跃请求/流、lifetime 与最近一小时成功率/延迟、端点/模型/Tier/状态码聚合，以及 60 个每分钟序列点。 |
| `usage` | Token 的 `lifetime` 与 `last_hour` 统计。包含 `requests`、`reported`、`coverage`、总 Token 和按模型/Tier 聚合。 |
| `upstream` | 上游尝试的 `lifetime`、`last_hour` 与 `recent`。按 Tier、匿名/Key 通道、Key 指纹聚合。 |
| `resources` | 模型目录、Key 冷却、脱敏代理节点、匿名开关和 models.dev metadata 状态。 |

`usage.*.tokens` 与模型/Tier 项均包含 `input_tokens`、`output_tokens`、`cached_tokens`、`reasoning_tokens`、`total_tokens`。`metrics.series` 的每分钟点也包含这些 Token 字段和 `usage_reported`。普通 JSON、同协议 SSE 与跨协议 SSE 响应都会解析上游 usage；`coverage` 是收到 usage 的推理请求数除以已建立上游路由的推理请求数。上游未提供 usage 时不会估算 Token。

每个 `upstream.recent` 项包含时间、Request ID、模型、Tier、尝试序号、匿名标记、通道、Key 指纹、`proxy_node`、HTTP 状态、耗时、成功标记和结果分类。匿名请求的 Key ID 固定显示为 `anonymous`；真实 Key 只显示 SHA-256 稳定短指纹。代理 URL 的认证信息会被移除，字段名称明确为代理节点而非出口 IP。

lifetime 从当前进程启动开始；last hour 使用 60 个一分钟 Bucket。管理响应最多返回最近 500 次一小时内上游尝试，WebUI 默认显示最后 100 次。数据不会写入配置、metadata 快取或其他数据库。

### Playground 与诊断 API

以下接口受管理 Session 保护；POST 还要求当前 CSRF Token：

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/api/debug/models` | 模型路由、原生协议、协议来源、Zen/Go 可用性、上游决定的匿名语义、成本与 metadata 状态。 |
| `POST` | `/api/debug/inference` | 通过真实 Gateway 发起 Chat、Responses 或 Anthropic 非流式诊断请求。 |

请求格式：

```json
{
  "protocol": "chat",
  "request": {
    "model": "example-model",
    "messages": [{"role": "user", "content": "Hello"}]
  }
}
```

`protocol` 可为 `chat`、`responses` 或 `anthropic`。服务端无条件把内层 `stream` 改为 `false`。诊断结果统一包含 `ok`、真实 `http_status`、`duration_ms`、`request_id`、`route` 和原始协议 `response`。一旦诊断调用已执行，管理接口本身返回 HTTP `200`，即使上游结果是 4xx/5xx；因此错误正文、路由和 Request ID 仍可一起查看。外层请求格式、Session、CSRF 或限速错误仍使用相应管理 HTTP 状态。

诊断响应不会包含本地 Server Key、上游 Key、Cookie、密码、Authorization 或代理凭据；响应中同名敏感字段及已配置敏感值会在返回浏览器前清除。最近一次结果只保存在当前管理进程内。

Docker 与 Zeabur 默认让 WebUI 和 API 共用同一个公开端口：`/` 和 `/api/` 由 WebUI 处理，`/v1/` 和 `/healthz` 由 API 处理。只有显式把 `LISTEN_ADDRESS` 与 `WEBUI_LISTEN_ADDRESS` 设置为不同地址时，才会启动两个监听端口。

## 编译

需要 Go 1.24 或更高版本。

```bash
go build -o opencode2api ./
```

## 下载

预编译的 Windows、Linux 和 macOS 可执行文件可从 [本 fork 的 GitHub Releases](https://github.com/Youzini-afk/opencode2api/releases) 下载。

向本 fork 推送符合 SemVer 的 `v*` 标签后，Release workflow 会同时发布六个平台压缩包与 `linux/amd64`、`linux/arm64` 的 `ghcr.io/youzini-afk/opencode2api` 镜像。普通 `main` 推送不会创建 Release 或覆盖镜像标签。

## Docker Compose 部署

服务器安装 Docker 与 Docker Compose 后，可以在克隆项目后直接构建并启动。推荐先创建 `.env`，其中只放自己的密钥，不要提交到仓库：

```bash
git clone https://github.com/Youzini-afk/opencode2api.git
cd opencode2api
cp .env.example .env
# 编辑 .env，替换本地 API key 和 WebUI 密码；Zen/Go key 按需填写
docker compose up -d
```

健康检查、WebUI 和日志：

```bash
curl http://127.0.0.1:8080/healthz
# 浏览器打开 http://127.0.0.1:8080
docker compose logs -f
```

容器只公开一个端口，宿主机端口可单独修改：

```bash
OPENCODE2API_PORT=18080 docker compose up -d
```

Compose 只挂载 `opencode2api-state` 状态卷，不再挂载源码或 Go 编译缓存。首次启动会把环境变量生成的配置写到卷内的 `/var/lib/opencode2api/config.json`；以后即使 `.env` 仍存在，也会优先读取该文件，因此 WebUI 修改不会在重启时被环境变量覆盖。

如果继续手工维护已有 `config.json`，可以在首次 `up` 前将其放进同一个命名卷，然后正常启动：

```bash
docker compose build
docker compose run --rm --no-deps -v "./config.json:/tmp/config.json:ro" --entrypoint sh opencode2api -c 'cp /tmp/config.json /var/lib/opencode2api/config.json'
docker compose up -d
```

如果配置文件不存在且没有设置任何 `OPENCODE2API_*` 初始化变量，容器会报错退出，不会使用公开的示例 API key 或管理密码启动。复制并填写 `.env.example`，或者先导入已有 `config.json` 后再启动。

## Zeabur Git 部署

1. 在 GitHub fork 本仓库，在 Zeabur 从该 fork 创建 Git 服务；仓库根目录的 `Dockerfile` 会在构建阶段编译二进制，运行时镜像不含 Go 工具链，也不需要源码挂载。
2. 为服务配置下面的初始化环境变量。Zeabur 注入的 `PORT` 会被自动用于 API 与 WebUI 的同一个 `0.0.0.0:<PORT>` 监听地址，不需要手工设置 `PORT`、`LISTEN_ADDRESS` 或 `WEBUI_LISTEN_ADDRESS`。
3. 如需在重新部署后保留 WebUI 修改的配置，为服务挂载持久卷到 `/var/lib/opencode2api`。没有持久卷也能启动，但容器文件系统重建后会再次从环境变量初始化。

| 环境变量 | 要求与格式 |
| --- | --- |
| `OPENCODE2API_SERVER_KEYS` | 必需。本服务对客户端校验的本地 API key；可填 JSON 字符串数组，或逗号/换行分隔列表。 |
| `OPENCODE2API_ZEN_KEYS` | 可选。Zen 上游 key 列表；格式同上。留空时使用匿名 Zen。 |
| `OPENCODE2API_GO_KEYS` | 可选。Zen Go 上游 key 列表；格式同上。 |
| `OPENCODE2API_WEBUI_USERNAME` | 可选，默认 `admin`。 |
| `OPENCODE2API_WEBUI_PASSWORD` | WebUI 启用时必需，至少 10 个字符。首次启动后会转换为 Argon2id 哈希。 |
| `OPENCODE2API_WEBUI_ENABLED` | 可选，默认 `true`；设为 `false` 可关闭 WebUI。 |

这些初始化变量只在配置文件不存在时读取。一旦 `/var/lib/opencode2api/config.json` 已生成，配置文件始终优先；更改初始化变量不会覆盖 WebUI 保存的内容。需要重新初始化时，应先明确备份并删除持久卷中的配置文件。

`OPENCODE2API_ZEN_KEYS` 和 `OPENCODE2API_GO_KEYS` 可以同时留空。此时服务匿名读取 Zen `/v1/models` 返回的完整目录，不按 `-free` 或其他模型名称筛选；模型是否允许匿名推理由上游决定，需要付费或鉴权的模型可能直接返回上游的 HTTP `401` 或 `403`。

部署后的同一个公开域名同时提供 WebUI `/`、管理接口 `/api/`、模型与推理接口 `/v1/`，健康检查路径是 `/healthz`。启动后模型目录首次刷新完成前，`/healthz` 短暂返回 `503 starting` 属于正常状态；进程已在监听，刷新成功后会变为 `200 ok`。

## 配置

复制示例配置：

```bash
cp config.example.json config.json
```

然后编辑 `config.json`：

```json
{
  "listen": "127.0.0.1:8080",
  "server_keys": ["change-this-local-key"],
  "zen_keys": [],
  "go_keys": [],
  "anonymous": false,
  "prefer": "go",
  "proxyfile": "",
  "proxies": ["direct"],
  "upstream": {
    "zen": "https://opencode.ai/zen",
    "go": "https://opencode.ai/zen/go"
  },
  "fingerprint": {
    "versions": [
      "1.18.18", "1.18.17", "1.18.16", "1.18.15", "1.18.14", "1.18.13",
      "1.18.12", "1.18.11", "1.18.10", "1.18.9", "1.18.8", "1.18.7"
    ]
  },
  "retry": {
    "max_attempts": 3,
    "timeout_seconds": 300
  },
  "models": {
    "refresh_seconds": 300,
    "protocols": {}
  },
  "performance": {
    "max_idle_conns": 2048,
    "max_idle_conns_per_host": 256,
    "max_conns_per_host": 0,
    "idle_conn_timeout_seconds": 120,
    "connect_timeout_seconds": 5,
    "failure_cooldown_seconds": 15
  },
  "logging": {
    "level": "info",
    "ring_size": 2000
  },
  "webui": {
    "enabled": true,
    "listen": "0.0.0.0:8081",
    "username": "admin",
    "password": "change-this-admin-password",
    "session_ttl_minutes": 720
  }
}
```

### 基础字段

| 字段 | 含义 |
| --- | --- |
| `listen` | 本地监听地址。默认建议使用 `127.0.0.1:8080`，避免服务直接暴露到公网。 |
| `server_keys` | 调用本代理时使用的本地 API key 列表。它们只用于本地鉴权，不会发送给 OpenCode。 |
| `zen_keys` | 可选的 OpenCode Zen API key 池。允许配置多个 key；空数组表示使用匿名 Zen。 |
| `go_keys` | 可选的 OpenCode Zen Go API key 池；没有 Go key 时使用空数组。 |
| `anonymous` | 是否在存在 Zen key 时仍优先尝试真正无凭证的匿名 Zen。没有 Zen key 时无论该值为何都会自动启用 keyless Zen。 |
| `prefer` | 模型同时存在于 Zen 与 Go 时优先使用的 key 上游，值为 `go` 或 `zen`，默认 `go`。不会因为普通失败自动跨到另一个 Tier。 |
| `proxyfile` | 可选代理池文件路径。相对路径以 `config.json` 所在目录为基准；内容会追加到 `proxies` 并去重。 |
| `proxies` | 上游代理列表。支持 `direct`、`http://`、`https://`、`socks5://` 和 `socks5h://`。URL 可以包含代理用户名和密码。 |

`server_keys` 至少需要一个值。`zen_keys` 和 `go_keys` 都可以为空；这只省略上游凭据，不会放宽调用本代理所需的本地 API key 或 WebUI 鉴权。

### Zen 匿名模式

keyless Zen 不发送 `Authorization` 或 `x-api-key`，也不会用 `public` 之类的哨兵凭证。`/v1/models` 始终返回实际上游发现的完整目录，不根据 `-free`、价格、提供商、弃用标志或 Gemini 名称筛选；推理资格由上游真实响应决定。

没有 Zen key 时自动使用匿名 Zen。存在 Zen key 且 `anonymous: true` 时，Zen 目录中的模型先走匿名通道：传输错误和 5xx 在 `retry.max_attempts` 内重试；`401/403` 及其他非 429 的 4xx 立即返回；只有匿名阶段出现 `429` 时，才回退到 `prefer` 选中的一个可用 key Tier。普通 key 请求不会因为失败自动跨 Zen/Go Tier。监控中的 `proxy_node` 表示所选代理节点，不代表或推断实际出口 IP。

models.dev 使用固定 30 秒超时，每 24 小时刷新一次。标准地址为 `https://models.dev/api.json`；规范化后的 OpenCode 模型成本缓存在 `config.json.models.dev.json`，以 `0600` 权限和同目录临时文件原子替换。启动会先读取可用快取，再尝试联网更新；更新失败不会丢弃旧资料，错误、更新时间与过期状态可在监控资源和诊断页查看。

### key 与代理分配规则

只需要直连时使用：

```json
"proxies": ["direct"]
```

SOCKS5 代理示例：

```json
"proxies": ["socks5://127.0.0.1:1080"]
```

多个代理示例：

```json
"proxies": [
  "http://user:password@127.0.0.1:7890",
  "socks5://127.0.0.1:1080"
]
```

也可以从文本文件加载代理池：

```json
{
  "proxyfile": "proxies.txt",
  "proxies": ["direct"]
}
```

`proxies.txt` 每行填写一个代理。支持空行、以 `#`、`;` 或 `//` 开头的整行注释，也支持在代理后使用空格加这些标记写行尾注释：

```text
# HTTP 代理
http://user:password@127.0.0.1:7890
socks5://127.0.0.1:1080  # 备用代理
```

配置中的 `proxies` 会先加载，随后加载 `proxyfile`，重复项只保留第一次出现的位置。如果两个来源都为空，则仍使用 `direct`。`config.json` 本身支持 `//` 单行注释和 `/* ... */` 块注释；引号内的 `https://` 等内容不会被当作注释。

### `upstream`

| 字段 | 含义 |
| --- | --- |
| `upstream.zen` | Zen 上游根地址，通常保持为 `https://opencode.ai/zen`。 |
| `upstream.go` | Zen Go 上游根地址，通常保持为 `https://opencode.ai/zen/go`。 |

### `retry`

| 字段 | 含义 |
| --- | --- |
| `retry.max_attempts` | 单个请求在所选通道内的最大尝试次数，包含第一次请求。Key 通道遇到网络错误、认证失败、限流或 5xx 会切换节点；其他 4xx 直接返回。匿名通道同样受此上限约束，且只有出现 `429` 才允许回退到一个首选 Key Tier。 |
| `retry.timeout_seconds` | 单个客户端请求的总超时时间，同时用于限制上游响应头等待时间。 |

流式响应一旦已经向客户端输出数据，就不会切换节点重新生成，避免拼接两个不同的响应。

### `fingerprint`

| 字段 | 含义 |
| --- | --- |
| `fingerprint.versions` | OpenCode CLI 版本池。每项必须是三段数字版本号；空白会清理，重复项只保留一次，且至少保留一项。 |

代理会按会话通过 rendezvous（最高随机权重）哈希稳定选择一个版本，并发送严格的 `User-Agent: opencode/<version>`。相同会话和版本池在热重载后不会漂移；增删版本时只重新分配受该版本影响的部分会话。

### `models`

| 字段 | 含义 |
| --- | --- |
| `models.refresh_seconds` | 重新读取 Zen 和 Go 模型列表的间隔秒数。两个列表会并发刷新。 |
| `models.protocols` | 手动指定模型的原生协议。值只能是 `chat`、`responses` 或 `anthropic`。通常保持为空。 |


模型协议覆盖示例：

```json
"protocols": {
  "custom-model": "chat"
}
```

模型同时存在于 Zen 与 Go 时按 `prefer` 配置选择：值为 `go` 时优先 Go，值为 `zen` 时优先 Zen（默认 `go`）。仅存在于某一池时使用该池。没有 Zen key 时，服务仍匿名读取并完整返回 Zen `/v1/models` 的目录，不根据 `-free` 后缀、提供商或其他模型名称筛选。具体模型是否允许匿名推理由上游响应决定；需要付费或鉴权的模型可能返回上游 HTTP `401` 或 `403`。

`deepseek-v4-flash-free` 默认使用 Chat 原生协议。与所有模型一样，`models.protocols` 中的显式映射优先于该默认值和名称推断。

### Thinking 工具历史兼容

所有请求都会经过同一个上游请求准备流程，同协议转发和跨协议转换不再使用两套分支。通过 Chat Completions 或 Anthropic Messages API 调用 DeepSeek、Kimi/Moonshot 或 MiMo 模型时，代理会按上游的目标协议规范化 assistant 工具历史：Chat 补全缺失或空的 `reasoning_content`；Anthropic 保留有效 thinking 文本、为缺失或空的 thinking 补充兼容占位内容、将 `redacted_thinking` 转为普通 thinking，并移除这些兼容端点不接受的 `signature`。显式启用 reasoning/thinking 的别名模型也会启用该处理，普通非 reasoning 请求不会被修改。

跨协议桥接会区分 Chat/Responses 的 system 与 developer 指令，在 Anthropic 目标中按顺序合并为 system 内容；reasoning effort 会转换为 adaptive thinking 与对应 effort。工具选择、空参数 `{}`、停止原因，以及 SSE 中延迟到达的工具名称、参数分片和完成事件也会转换到目标协议的对应形态；Anthropic 内容块会严格按 start/delta/stop 顺序串行输出。

### `performance`

| 字段 | 含义 |
| --- | --- |
| `performance.max_idle_conns` | 所有上游连接池允许保留的最大空闲连接数。 |
| `performance.max_idle_conns_per_host` | 每个上游主机允许保留的最大空闲连接数。 |
| `performance.max_conns_per_host` | 每个主机的最大并发连接数。`0` 表示不设置上限。 |
| `performance.idle_conn_timeout_seconds` | 空闲连接在连接池中保留的时间。 |
| `performance.connect_timeout_seconds` | 与上游或代理建立 TCP 连接的超时时间。 |
| `performance.failure_cooldown_seconds` | 连接失败、认证失败、限流或 5xx 后节点的基础冷却时间。连续失败会指数增加冷却时间。 |

### `logging`

| 字段 | 含义 |
| --- | --- |
| `logging.level` | 日志级别，支持 `debug`、`info`、`warn` 和 `error`，可通过 WebUI 热切换。 |
| `logging.ring_size` | WebUI 最近日志环容量，范围 100–50000，默认 2000。stdout 不受此容量限制。 |

每条 stdout 日志都是单行 JSON，包含时间、级别、组件、事件以及适用的 request ID、模型、tier、状态码、耗时和重试次数。普通“请求完成”事件使用 `debug` 级别，避免 `info` 日志被每个成功请求淹没；警告和错误仍按原级别输出。日志不会输出完整上游 key、本地 key、Authorization、Cookie、代理认证信息或请求消息正文。

### `webui`

| 字段 | 含义 |
| --- | --- |
| `webui.enabled` | 是否启动管理服务。旧配置未包含该段时默认关闭。 |
| `webui.listen` | 管理服务监听地址；与顶层 `listen` 完全相同时共用一个 HTTP server，不同时使用独立端口。示例为 `0.0.0.0:8081`。 |
| `webui.username` | 单一管理员账号。 |
| `webui.password` | 仅用于首次初始化的明文密码，至少 10 个字符；启动后自动删除。 |
| `webui.password_hash` | 自动生成的 Argon2id 哈希，不应手动编辑，也不会由 WebUI API 返回。 |
| `webui.session_ttl_minutes` | 登录 session 有效时间，范围 5–10080 分钟。 |

WebUI 中普通配置响应只包含 key 尾码/指纹及脱敏 proxy。需要查看完整值时必须再次输入管理密码，敏感响应禁止浏览器缓存。

### 配置保存与热重载

WebUI 保存时先解析并验证完整候选配置、创建新的连接池和 Gateway，然后写入临时文件、保留 `config.json.bak` 并替换 `config.json`，最后原子切换新请求使用的运行实例。写入或初始化失败时旧实例继续工作；切换前已开始的请求不会中断。

keys、proxy、上游、指纹版本池、重试、模型、性能、优先 tier 和日志级别会立即生效。`listen`、`webui.listen` 与 `webui.enabled` 会保存但需要重启进程。WebUI 也提供“从磁盘重载”，外部编辑后的配置仍会经过相同的验证与回滚流程。保存后的 JSON 会被规范化，原有注释不会保留。


## 会话 ID

代理会为上游添加 OpenCode 使用的 `User-Agent`、`x-opencode-client`、`x-opencode-session`、`x-opencode-request` 和 `x-opencode-project` 请求头。

- `User-Agent` 严格为 `opencode/<version>`；版本从 `fingerprint.versions` 按会话稳定选取，不包含运行本服务的操作系统或 Go 版本。
- 每个请求使用不同的 `x-opencode-request`，同一次请求的重试保持不变。
- 优先使用客户端提供的 `x-opencode-session`、`x-session-id`、`conversation-id`、`conversation_id` 或 `metadata.session_id` 生成会话 ID。
- 没有显式会话标识时，使用第一条用户消息生成稳定会话 ID，使同一段多轮对话保持一致。
- `x-opencode-project` 默认由会话稳定生成 40 位小写 SHA-1；显式传入 `global` 或合法的 40 位小写 SHA-1 时原样保留，其他非空值会稳定规范化为 SHA-1。
- 如果两个独立会话的第一条消息完全相同，建议由客户端发送不同的 `x-session-id`，以确保两个会话严格分离。

## 致谢

感谢 [LINUX DO](https://linux.do) 社区一直以来的支持。
