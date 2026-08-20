# WeKnora 2026-08-20 生产发布与 Docker 回滚交接

## 1. 发布结论

- 发布结论：`GO`。
- 生产地址：`https://know.seeway.co`。
- 远端目录：`/home/fox/WeKnora`。
- 源码分支：`codex/jiwai-branding`。
- 部署源码提交：`3df8ccbfe1ca7fd423e2d8c0ddcd3f1c7e347544`。
- 数据库：migration `87`，`dirty=false`。
- 发布时间：2026-08-20，Asia/Shanghai。
- 备份例外：按发布指令停止并删除了本轮未完成的新备份，保留并复用
  `/home/fox/WeKnora/backups/releases/20260819-154754`。该例外不影响普通镜像回滚，
  但灾难数据恢复的 RPO 固定在 2026-08-19 15:47:54 左右。

## 2. 最终运行资产

| 服务 | 镜像标签 | 镜像 ID | 状态 |
|---|---|---|---|
| App | `wechatopenai/weknora-app:deploy-3df8ccbf` | `sha256:e62d8f53594d3348b2b5a0972faa62a687917f08b4ed286f7d7833f015cdb0d6` | healthy，restart 0 |
| Frontend | `wechatopenai/weknora-ui:deploy-3df8ccbf` | `sha256:b4bdc4cf35761ca151f760afc8cb5f8546c4e80000e670c4ee0953abd35e8271` | healthy，restart 0 |
| DocReader | `wechatopenai/weknora-docreader:deploy-3df8ccbf` | `sha256:16e9ee8f5313f39fa0f83fb7d48070ef95323fd23c8e675aa9e552a916ffd8c1` | healthy，restart 0 |

发布完成后的关键资源基线：

- App RSS：约 241 MiB。
- Frontend RSS：约 11 MiB。
- DocReader RSS：约 159 MiB。
- PostgreSQL RSS：约 257 MiB。
- Redis RSS：约 13 MiB。
- MinIO RSS：约 215 MiB。
- MinerU：`3.4.5`，`pipeline` CPU 模式，`mineru-api.service` active，
  `NRestarts=0`，RSS 约 2.45 GiB。
- MinerU systemd 并发：`MINERU_API_MAX_CONCURRENT_REQUESTS=2`。
- 平台解析配置：`mineru_max_concurrency=2`。
- DocReader：gRPC，端口 `50051`。

## 3. 生产环境边界

生产 `.env` 当前必须保持：

```dotenv
WEKNORA_VERSION=deploy-3df8ccbf
AUTO_MIGRATE=false
AUTO_RECOVER_DIRTY=false
KNOWLEDGE_UPLOAD_MAX_FILE_SIZE_MB=2048
KNOWLEDGE_UPLOAD_CHUNK_SIZE_MB=4
KNOWLEDGE_UPLOAD_SESSION_TTL_HOURS=24
```

- 未配置 `KNOWLEDGE_UPLOAD_RATE_BPS`，当前不执行 9 Mbps 人为限速。
- 网站默认同时上传一个文件，其余文件进入全局队列。
- 上传队列只在知识库页面展示，但 SPA 跳转期间任务继续运行。
- App 主进程不执行数据库迁移，也不自动恢复 dirty migration。
- `SSRF_WHITELIST_EXTRA` 保留原有可信私网目标，并补入 Compose 内部别名
  `docreader`、`searxng`、`qdrant`、`milvus`、`weaviate`、`doris-fe`、
  `doris-be` 和 `minio`。没有降低通用 SSRF 校验。
- 代理 `100.70.139.102:7890` 只用于 Git 和构建依赖下载，不用于生产解析流量。

## 4. 本次源码范围

本次发布从本地完整工作树形成原子提交，包含：

- Agent 成功、失败、停止互斥终态及持久化顺序。
- Quick、Agent、IM 的一致 outcome 语义。
- 上游 SSE EOF、读取错误和工具参数截断检测。
- SiliconFlow `deepseek-ai/DeepSeek-V4-Flash` 官方适配。
- refresh token 原子消费、事务轮换、随机 JWT `jti` 和 migration `000087`。
- 首次提问与迟到会话恢复竞态保护。
- 公司预置模型、平台解析引擎、模型设置和解析设置。
- 显式知识库目录、目录切换和空目录持久化。
- 2 GiB 上限、4 MiB 分片、断点续传和全局上传队列。
- DocReader 与解析器流式传输、资源限制和 SHA-256 身份。
- 浏览器扩展 `v1.3.2`、固定公司 API、动态网页采集和前端下载资源。
- 业务审计保留、认证凭据脱敏、文件代理和资源访问边界。

SiliconFlow 行为固定为：

- 仅精确匹配官方文档明确支持的 `deepseek-ai/DeepSeek-V4-Flash`。
- 思考开启时发送 `enable_thinking=true`，默认发送
  `reasoning_effort=high`。
- 思考关闭时发送 `enable_thinking=false`，不发送 `reasoning_effort`。
- 官方依据：
  [Reasoning 能力](https://api-docs.siliconflow.cn/docs/userguide/capabilities/reasoning)、
  [Chat Completions](https://api-docs.siliconflow.cn/docs/api/chat-completions-post)。

## 5. 自动化发布门禁

最终提交完成后重新执行并通过：

- `git diff --check`。
- `go test -count=1 ./...`。
- Agent、session、IM、chat、repository 和 service 定向 `go test -race`。
- Frontend 全量测试 `454/454`。
- Frontend 真实挂载测试 `3/3`。
- Frontend `npm run type-check` 和 `npm run build`。
- DocReader：`152 passed`、`12 skipped`，另有 5 个定向子测试通过。
- 浏览器扩展 `browser-extension/scripts/verify.sh` 全部通过。
- `docker compose config --quiet`。
- 真实 PostgreSQL：86 到 87、重复迁移、旧新 token 查询、并发 refresh
  恰好一次成功、revoke/logout、dirty fail-close 和 down 回滚均通过。
- 最终 App、Frontend、DocReader 镜像分别完成隔离健康验证。
- 最终一次综合只读审查无仍适用于当前生产的 P0/P1。

## 6. 实际发布步骤

实际发布按以下顺序完成：

1. 核对本地 HEAD、origin 和远端部署仓库。
2. 将远端仓库 fast-forward 到 `3df8ccbf`。
3. 固定 `AUTO_MIGRATE=false` 和 `AUTO_RECOVER_DIRTY=false`。
4. 使用固定 `migrate v4.19.1` 显式执行一次 `up 1`。
5. 核对 schema 从 `86 clean` 变为 `87 clean`。
6. 依次切换 DocReader、App、Frontend。
7. 每一步等待健康检查通过后再继续。
8. App 首次启动因部署 `.env` 覆盖 Compose 默认白名单且缺少 `docreader`
   别名而被 SSRF 校验阻止。仅补充可信内部别名后重新创建 App，未关闭校验。
9. 最终三容器均 healthy，restart 0，公网首页和 App health 返回 200。

后续同版本重建或重新部署时使用受控流程：

```bash
cd /home/fox/WeKnora
git fetch origin codex/jiwai-branding
git merge --ff-only origin/codex/jiwai-branding

# 先在受保护环境中加载数据库连接变量，再显式迁移。
# migrate 必须固定为 v4.19.1，且每次发布只允许明确的步数。
./scripts/migrate.sh version
./scripts/migrate.sh up 1

docker compose up -d --no-deps --force-recreate docreader
docker compose ps docreader
docker compose up -d --no-deps --force-recreate app
docker compose ps app
docker compose up -d --no-deps --force-recreate frontend
docker compose ps frontend
```

禁止把数据库密码或完整连接串写入命令历史。远端没有固定 `migrate v4.19.1`
时，应使用固定版本的临时迁移容器，并从受保护环境注入连接变量。

## 7. ego 真实验收

### 7.1 桌面核心流程

- 新建云端向量知识库
  `ebbb1a5d-774b-4bde-9dbf-9f0397e6bec1`，使用公司
  `Qwen/Qwen3-VL-Embedding-8B`，未使用本地向量模型。
- 新建目录弹窗约 132 ms 打开，提交后约 59 ms 可见。
- 根目录与 `验收目录A` 切换分别约 442 ms 和 439 ms，右侧内容同步变化。
- DeepSeek-V4-Flash Quick QA 约 2 秒返回，正确回答 DocReader 端口
  `50051` 和扩展版本 `1.3.2`，带 5 条引用。
- 刷新后标题、正文、引用和 AgentSteps 均保留。
- Agent 成功、模型错误、用户停止各执行一次，消息表与终态一致。
- 模型错误只收到唯一 `error done=true`，没有错误后继续发送 `complete`。
- 上传队列在知识库页存在，在聊天页不存在。

### 7.2 新账号完整流程

- 专用验收用户 ID：`5ba02bc1-3cbb-493b-ab70-34e9af918b3c`。
- 专用空间 ID：`10007`。
- 注册 201，首次登录 200，`/auth/me` 200。
- refresh 成功轮换，旧 refresh token 重放返回 401。
- logout 后旧 access token 调 `/auth/me` 返回 401，再次登录成功。
- 新账号可见 8 个公司预置模型，全部 `is_builtin=true`。
- 新账号可见 9 个解析引擎，MinerU 可用，DocReader transport 为 gRPC。
- 使用公司云端向量模型创建知识库，上传 PDF 后解析为 `completed`。
- DeepSeek 问答收到 28 个 SSE 事件、唯一 `complete`、2 条引用，刷新后
  回答仍包含 `50051`。
- 测试知识库、知识、会话已清理；账号保留用于审计，当前有效 token、
  活跃知识库、会话和知识数量均为 0。

### 7.3 文件与解析

- DOCX：MinerU 解析完成，合并单元格内容可读。
- 文本 PDF：MinerU 解析完成。
- 扫描 PDF：300 DPI 纯图片 PDF OCR 完成并提取 `50051`。
- 图片：PNG 通过 MinerU 和公司 VLLM 处理完成并提取 `50051`。
- 256 MiB PDF：实际大小 `268451977` 字节。
- 上传进行中切换到聊天页后任务持续；队列按钮按页面规则隐藏。
- 硬刷新后队列恢复为“等待选择原文件”，服务端确认位置为
  `188.0 MiB / 256.0 MiB（73%）`。
- 重新选择同一文件后从 73% 继续到 78%，没有从 0 重传，随后完成
  “上传中 -> 正在合并 -> 解析中 -> 已完成”。
- 上传会话和知识记录大小均为 `268451977`；MinIO stat 大小一致。
- 本地、数据库和 MinIO 流式重算 SHA-256 均为
  `2d237bbb28d169485476cc50e179f068e1f3806697f21b6b52a8cf69d8d10af9`。
- 验收结束后 6 个测试知识和对应对象已删除，用户并发上传的 3 个文件保留。

该轮把硬刷新视为浏览器连接中断并验证服务端断点恢复；没有把未能影响
Service Worker 连接的 CDP offline 探针记录成物理网络断线证据。

### 7.4 移动端

- `390x844`：知识库和聊天页均无水平溢出、固定元素越界或输入框遮挡。
- `375x812`：同样通过。
- 两个尺寸下，上传队列按钮只在知识库页出现，聊天输入框完整位于视口内。

### 7.5 浏览器扩展

- 生产 ZIP：`/downloads/jiwai-knowledge-assistant-1.3.2.zip`。
- 生产 CRX：`/downloads/jiwai-knowledge-assistant-1.3.2.crx`。
- ZIP SHA-256：`bb55a41e2c45ec9f661fabfa9e36a61003af47c86d05b61ab577ebbd1bb62e49`。
- CRX SHA-256：`b3d319b906ea922a26ff309bd740c0f28eed43a7e8ca965d9ca0bd4c229ddfca`。
- ZIP 完整性检查无错误，manifest 为 MV3、版本 `1.3.2`。
- 固定 API：`https://know.seeway.co/api/v1`。
- 临时 API Key 仅通过 `X-API-Key` 调 `/auth/me` 返回 200，并成功列出
  4 个知识库；撤销后同一请求返回 401。
- 临时 Key 已删除，明文未输出、未写入文件或文档。

## 8. 审计日志边界

业务审计按公司项目要求保留，不执行“只留元数据”的过度脱敏：

- `audit_logs` 能显示操作者、角色、动作、目标、结果和业务对象名称。
- 新账号验收日志可定位到具体操作者，并记录知识库名称和上传文件名。
- App 持久日志保留用户问题、检索正文、模型请求正文、图片 URL、业务设置值
  和第三方错误正文，便于追查员工操作和模型行为。
- 密码、API Key、Bearer/Basic Authorization、Cookie、私钥、签名和明确的
  secret/token 字段必须脱敏。
- 生产注入假凭据的验收结果：业务正文命中 7 次，操作者命中 1 次，
  脱敏标记命中 7 次，假凭据原文命中 0 次。
- 审计日志持久写入 `weknora_app-logs` Docker 卷；系统审计页面仅允许
  SystemAdmin 访问。

## 9. 回滚资产

保留备份：

```text
/home/fox/WeKnora/backups/releases/20260819-154754
```

- 总大小：约 13 GiB。
- 源码快照提交：`68d7cac5da0673f14e06a99ee38ba0fb431a105d`。
- 16 个运行镜像归档。
- 10 个 `weknora_*` Docker 卷归档。
- PostgreSQL custom dump 和 globals。
- `.env`、Compose、override、`config/`、旧 frontend `dist`、Git bundle。
- MinerU 3.4.5 环境包、systemd unit、drop-in、`pip freeze` 和 Conda explicit。
- `SHA256SUMS` 共校验 241 个文件，失败 0。

关键归档哈希：

| 资产 | SHA-256 |
|---|---|
| Git bundle | `9053287de762bccee0c7b89a44fc625773e4b4193fcf98e96699f89c09c87e4b` |
| PostgreSQL dump | `95b734ebd9fc1b6e7f5e5f5e2c0b004316f1dad8ea3f8c65ea8d35a191ae2621` |
| 运行镜像归档 | `ca8a5d3f7dd0e18bc349a75ca38a92e2acee72575f544cd4986bd0809dccda82` |
| MinerU 3.4.5 | `da991b51fa90c71eb0e5fcfc9f38e31fb950e734181e8b329645ed8f7ba80329` |
| MinIO 卷 | `5f212c1da812ce915cfeb075c8824475da455adaa71e0bbf51b8fdb757024f3a` |
| PostgreSQL 卷 | `0ad3b974193fb4931d5dede63b34778e340125730ab282027cff1e9d4dabbcfb` |

普通回滚镜像：

| 服务 | 回滚标签 | 镜像 ID |
|---|---|---|
| App | `wechatopenai/weknora-app:rollback-before-large-upload-20260819-154754` | `sha256:a0c29cec2745383c16243a33bfbdb06e1b6ce144e3771bba73a7e837b8786749` |
| Frontend | `wechatopenai/weknora-ui:rollback-before-large-upload-20260819-154754` | `sha256:89b76e77ae842f909e2723913eddc9de95664f433e3b8bc90beab384ec9fe234` |
| DocReader | `wechatopenai/weknora-docreader:rollback-before-large-upload-20260819-154754` | `sha256:b9c4636b65b5d4947d5e09cd311ba6cf37f1f2da37c51d4be2b911d432f12abe` |

## 10. 普通回滚步骤

普通回滚只切回配置和三个核心镜像，不降 schema 87。migration `000087` 为
expand 兼容，旧代码不使用新增字段时可以继续运行。

```bash
cd /home/fox/WeKnora
BACKUP=/home/fox/WeKnora/backups/releases/20260819-154754

# 保留当前配置现场，文件权限必须保持 600。
cp -p .env ".env.before-rollback.$(date +%Y%m%d-%H%M%S)"
install -m 600 "$BACKUP/config/.env" .env
install -m 644 "$BACKUP/config/docker-compose.yml" docker-compose.yml
install -m 644 "$BACKUP/config/docker-compose.override.yml" docker-compose.override.yml

# 只有本机缺少回滚镜像时才加载全量归档。
zstd -dc "$BACKUP/images/running-images-20260819-154754.tar.zst" | docker load

docker compose up -d --no-deps --force-recreate docreader
docker compose up -d --no-deps --force-recreate app
docker compose up -d --no-deps --force-recreate frontend
docker compose ps
```

回滚后必须核对：

```bash
docker inspect --format '{{.Name}} {{.Image}} {{.RestartCount}} {{.State.Status}}' \
  WeKnora-docreader WeKnora-app WeKnora-frontend
docker exec WeKnora-postgres psql -U postgres -d WeKnora \
  -c 'select version, dirty from schema_migrations;'
systemctl is-active mineru-api.service
curl -fsS https://know.seeway.co/ >/dev/null
```

## 11. 灾难回滚边界

只有数据库、MinIO 或卷数据损坏时才执行灾难回滚。该操作会覆盖现有数据，
必须先停止所有写入、取得明确审批并再做一次当前现场快照。

恢复顺序：

1. 停止 Frontend、App、DocReader 和所有写入 Worker。
2. 校验备份目录 `SHA256SUMS`。
3. 恢复 PostgreSQL dump 或 PostgreSQL 卷，二者只选一种主路径。
4. 恢复 MinIO、Qdrant、Neo4j、Langfuse 等所需卷。
5. 加载镜像归档并按 `manifests/docker-inspect.json` 恢复容器。
6. 恢复 MinerU unit、drop-in 和 3.4.5 环境。
7. 复核镜像 ID、卷挂载、schema、对象数量、核心健康和真实文件解析。

警告：该备份点早于本次发布约一天。灾难恢复会丢失备份时间之后产生的
数据库和对象存储数据。普通镜像回滚不触碰当前数据库或卷，不受该 RPO 影响。

## 12. 发布后计划与残余风险

以下为已明确延期的 P2/P3，不阻塞本次生产发布：

- 标题生成超过前端 3 秒等待窗口时，当前会话需刷新后显示新标题。本次实测
  标题约 6.2 秒完成。
- 执行 2 GiB 实际上传和 30 分钟以上浸泡测试。
- MinerU 并发 1/2/3/4/6 全档长稳压测；生产继续使用已验证并发 2。
- 浏览器扩展对全部办公网站逐站人工回归；当前已有自动化夹具和代表站点。
- PostgreSQL 远程 TLS/证书能力；当前数据库位于同机 Docker 内网。
- `auth_tokens` 大规模分批回填、并发索引和长期历史清理。
- 上传队列 localStorage 写入节流和移动端完整目录抽屉。
- 本轮依发布指令复用 2026-08-19 备份，下一次变更前应重新生成当日完整备份，
  将灾难恢复 RPO 收敛到发布前。

## 13. 交接检查命令

```bash
cd /home/fox/WeKnora
git rev-parse HEAD
git status --short
docker compose ps
docker stats --no-stream WeKnora-app WeKnora-frontend WeKnora-docreader
systemctl show mineru-api.service -p ActiveState -p NRestarts -p MemoryCurrent
docker exec WeKnora-postgres psql -U postgres -d WeKnora \
  -c 'select version, dirty from schema_migrations;'
```

正常结果：源码为交接提交、工作树无未解释修改、三核心容器 healthy、restart 0、
MinerU active 且 `NRestarts=0`、schema `87 false`。
