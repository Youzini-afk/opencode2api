# Fork 部署与兼容说明

此 fork 保留 Zeabur 单端口、首次环境初始化、无 Key 全量目录、429 回退与会话指纹能力。代码迁移到 cmd/internal 结构后，原有 config.json 与持久卷可继续使用。

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

| 环境变量                      | 要求与格式                                                                          |
| ----------------------------- | ----------------------------------------------------------------------------------- |
| `OPENCODE2API_SERVER_KEYS`    | 必需。本服务对客户端校验的本地 API key；可填 JSON 字符串数组，或逗号/换行分隔列表。 |
| `OPENCODE2API_ZEN_KEYS`       | 可选。Zen 上游 key 列表；格式同上。留空时使用匿名 Zen。                             |
| `OPENCODE2API_GO_KEYS`        | 可选。Zen Go 上游 key 列表；格式同上。                                              |
| `OPENCODE2API_WEBUI_USERNAME` | 可选，默认 `admin`。                                                                |
| `OPENCODE2API_WEBUI_PASSWORD` | WebUI 启用时必需，至少 10 个字符。首次启动后会转换为 Argon2id 哈希。                |
| `OPENCODE2API_WEBUI_ENABLED`  | 可选，默认 `true`；设为 `false` 可关闭 WebUI。                                      |

这些初始化变量只在配置文件不存在时读取。一旦 `/var/lib/opencode2api/config.json` 已生成，配置文件始终优先；更改初始化变量不会覆盖 WebUI 保存的内容。需要重新初始化时，应先明确备份并删除持久卷中的配置文件。

`OPENCODE2API_ZEN_KEYS` 和 `OPENCODE2API_GO_KEYS` 可以同时留空。此时服务匿名读取 Zen `/v1/models` 返回的完整目录，不按 `-free` 或其他模型名称筛选；模型是否允许匿名推理由上游决定，需要付费或鉴权的模型可能直接返回上游的 HTTP `401` 或 `403`。

部署后的同一个公开域名同时提供 WebUI `/`、管理接口 `/api/`、模型与推理接口 `/v1/`，健康检查路径是 `/healthz`。启动后模型目录首次刷新完成前，`/healthz` 短暂返回 `503 starting` 属于正常状态；进程已在监听，刷新成功后会变为 `200 ok`。

## 与上游不同的路由约定

无 Zen Key 自动匿名，不需要个人上游 Key。官方匿名端点使用公开的 `public` 标记；自定义中转默认不发送认证头。匿名 401/403 直接返回；只有 429 才回退一个首选 Key Tier。普通 Key 请求不跨 Tier。默认目录不按模型名、成本、弃用、能力缺失或探测失败筛选；新增 `models.free_only` 只有显式开启才筛选。

官方端点自动使用新版 CLI 请求形状与官方工具定义，自定义中转仅在显式开启 `upstream.agent_shaping` 后改形。`upstream.anonymous` 可单独配置匿名推理地址；旧配置中的官方 Zen 自动升级到 inference 地址，自定义 Zen 默认沿用原地址，不会被转发到官方服务器。模型探测由 `models.probe_availability` 控制，默认关闭；探测只影响诊断信息，手动控制可暂停探测。

会话稳定版本池与 project 分桶继续保留；UA 更新为 `opencode/latest/<version>/cli`，session 采用降序编码并发送两个一致的 session 头。本地 request ID 仍保持每请求唯一和重试稳定，但不再作为旧 `x-opencode-request` 头发送上游。已有版本池不会被升级覆盖。

System One、动态协议发现、模型与成本元数据、轮转别名、额度查询和 API Key 管理均保留。用量新增按本地 API Key 和会话统计，热重载保留，进程重启清零。
