# WeKnora 当前版本 Docker 部署与验收手册

最后现场核验：2026-08-18 18:03（Asia/Shanghai）

本文记录见外传媒当前 WeKnora 分支在远端生产机上的 Docker 部署方式、配置边界、验收门槛和回滚方法。所有命令均不得包含 SSH 密码、API Key、数据库密码或模型凭据。

## 1. 当前生产基线

| 项目 | 当前值 |
| --- | --- |
| 本地仓库 | `/Users/xingranya/Downloads/GitHub-clone/WeKnora` |
| 远端主机 | `fox@100.78.64.62` |
| 远端仓库 | `/home/fox/WeKnora` |
| Git 远端 | `https://github.com/xingranya/WeKnora.git` |
| 生产分支 | `codex/jiwai-branding` |
| 生产功能提交 | `5b492baa5dbee50744c9532ccdc1623939287245` |
| Compose 项目名 | `weknora` |
| 前端地址 | `http://100.78.64.62:8081` |
| 后端地址 | `http://100.78.64.62:8080` |
| 构建代理 | `http://100.70.139.102:7890`，仅临时传入构建命令 |
| Debian 镜像 | `mirrors.aliyun.com` |
| Rust 镜像 | `https://rsproxy.cn` |
| SSH ED25519 指纹 | `SHA256:nppGrxGZS68MpWiCT6zq7AHOqYZnwpNoqCwmrkDiH7w` |

### 1.1 核心镜像

| 服务 | Compose 期望标签 | 当前实际容器标签 | 状态 |
| --- | --- | --- | --- |
| app | `wechatopenai/weknora-app:deploy-5b492baa` | `deploy-eb02a753` | healthy，重启 0 |
| frontend | `wechatopenai/weknora-ui:deploy-5b492baa` | `deploy-5b492baa` | running，重启 0 |
| docreader | `wechatopenai/weknora-docreader:deploy-5b492baa` | `deploy-2be2572` | healthy，重启 0 |

本次 `5b492baa` 发布浏览器插件 `v1.3.1`，修复巨量引擎帮助中心通过 Shadow DOM 延迟挂载飞书 iframe 时只采集外壳的问题，并改进飞书虚拟滚动、附件与图片占位处理。frontend 已切换到最终标签，镜像清单摘要为 `sha256:03c812de6fe85e412ea924b2e2942c460cf61dc2fc1a20f617c3cdf400445230`；app 与 docreader 未变化且未重启，当前健康镜像已增加 `deploy-5b492baa` 别名。

### 1.2 当前关键环境开关

远端 `.env` 已现场确认以下非敏感项：

```dotenv
WEKNORA_VERSION=deploy-5b492baa
AUTO_MIGRATE=true
STORAGE_TYPE=minio
SSRF_WHITELIST_EXTRA=searxng,qdrant,milvus,weaviate,doris-fe,doris-be,host.docker.internal,minio,192.168.0.20
```

其余 `.env` 内容包含数据库、Redis、MinIO、JWT、模型和集成凭据，不得写入 Git、命令参数、构建日志或本手册。

### 1.3 当前运行组件与专用文件

当前运行组件包括 app、frontend、docreader、PostgreSQL、Redis、MinIO、Qdrant、Neo4j、SearXNG、Langfuse Web/Worker/ClickHouse/MinIO 和 MCP Server。

以下文件和目录只属于远端运行机，已通过 `.git/info/exclude` 与源码状态隔离，拉取和切换目录时必须保留：

```text
.env
.env.before-*
docker-compose.override.yml
docker-compose.override.yml.before-*
misc/
backups/
```

## 2. 当前版本包含的定制能力

- 见外传媒品牌、应用壳层和多语言文案。
- Skill 页面、见外知识库 ZIP 下载和自动安装提示；当前版本为 `v1.2.1`，Windows 脚本使用 UTF-8 BOM 和 CRLF，兼容 Windows PowerShell 5.1 解析。
- 见外知识库助手 `v1.3.1`，固定公司服务地址，用户只填写 API Key；普通用户默认下载 ZIP 解压安装，CRX3 仅保留给扩展商店和企业策略分发。
- 巨量引擎帮助中心的外层 `tt-docs-component` 只作为待完成壳处理；后台等待 Shadow DOM 内跨域飞书 iframe 就绪并多轮读取，完整飞书结果优先于短外壳结果。
- 飞书虚拟文档按约 0.9 个可视高度逐段滚动并等待可见资源稳定，保留真实附件封面和可访问图片；来源站自身的加载失败 SVG 不再膨胀为伪图片，而是转为明确的未加载说明。
- 文档集采集可识别侧栏、折叠目录、组件树路由、分类页和虚拟滚动，递归发现同站正文并逐篇独立入库；支持暂停、继续、取消、重试、去重和重启恢复，默认最多 50 篇。
- 手动框选支持选区移动、八向缩放和自动滚动长截图；截图以内嵌图片随 Markdown 一起写入知识库。
- 动态网页采集支持 Shadow DOM、跨域 frame 聚合和完整虚拟滚动，按飞书块语义保留标题、表格、列表、Callout、链接和图片。
- blob 图片转为 data URI 后分块传输；后端在 32MB 总请求边界内转存最多 100 张内嵌图片，并将 metadata 改写为稳定 `resource://` 地址。
- Sub2API `reasoning_effort` 思考参数适配。
- 公司预置模型权限、脱敏、系统管理员维护和模型测试限制。
- `adopt_existing_model_ids` 声明式采用现有公司模型，保留数据库参数和加密凭据。
- 平台级解析引擎配置及迁移 `000085`。
- 知识库、临时文档、聊天附件统一使用平台解析配置。
- 模型设置、解析引擎设置、API、国际化和回归测试更新。
- Docker 构建支持 Debian、Rust 镜像参数。

关键提交：

```text
5b492ba fix(browser-extension)：优化巨量文档加载与飞书内容采集
deaa8ed feat(extension): 支持文档集与长网页采集
eb02a75 fix(extension): 改用 ZIP 解压安装
ed89212 fix(images): 扩大内嵌图片处理上限
36c901f feat(extension): 保留动态文档富媒体
01856ae feat(extension): 品牌化见外浏览器插件
7aa20c9 fix(skill): 修复 Windows 脚本解析错误
a07a218 style(models): 格式化预置模型测试
1a88f7d feat(models): 采用现有公司预置模型
2be2572 fix(docker): 支持配置 Rust 构建镜像
0e9f677 feat(platform): 统一公司预置资源与解析配置
4f17202 feat(chat): 适配 Sub2API reasoning_effort 思考参数
```

## 3. 部署安全边界

1. 本地工作区是代码来源，部署前必须检查 `git status`、提交、未跟踪文件和 origin。
2. 远端 `/home/fox/WeKnora` 必须保持为 Git 仓库，不再使用零散文件白名单同步。
3. SSH 必须使用严格主机校验，禁止 `StrictHostKeyChecking=no`。
4. 密码只在 SSH 交互提示中输入，禁止放入命令、脚本、文档或日志。
5. `.env`、数据库和 Docker volume 属于运行数据，拉取代码时不得覆盖或删除。
6. 构建成功前不切换运行容器；切换顺序固定为 app、frontend、docreader。
7. app 未恢复 healthy 前不得继续切换其他服务。
8. 任何模型采用、迁移和清理操作必须先生成可恢复备份。

## 4. 部署前检查

### 4.1 本地检查

```bash
cd /Users/xingranya/Downloads/GitHub-clone/WeKnora
git status --short --branch
git log -5 --oneline --decorate
git diff --check
git remote -v
```

正式部署只接受以下状态：

- 当前分支明确。
- 工作树中的功能修改已经审核、测试并提交。
- 本地 HEAD 与 `origin/codex/jiwai-branding` 一致。
- 不存在误加入的 `.env`、密钥、缓存、构建目录或临时文件。

### 4.2 SSH 身份检查

首次连接或主机密钥变化时：

```bash
ssh-keyscan -T 5 -t ed25519 100.78.64.62 | ssh-keygen -lf -
ssh-keygen -F 100.78.64.62 -f ~/.ssh/known_hosts
```

指纹必须与本手册基线一致，再执行：

```bash
ssh -o StrictHostKeyChecking=yes fox@100.78.64.62
```

### 4.3 远端运行基线

```bash
cd /home/fox/WeKnora
git status --short --branch
git rev-parse HEAD
docker compose -p weknora config --images
docker compose -p weknora ps
df -h /home/fox/WeKnora
```

## 5. 备份

### 5.1 源码和环境配置

历史完整源码备份：

```text
/home/fox/WeKnora-backup-20260817-215603
```

每次部署前增加独立 `.env` 备份：

```bash
cd /home/fox/WeKnora
DEPLOY_TS=$(date +%Y%m%d-%H%M%S)
cp -a .env ".env.before-deploy-${DEPLOY_TS}"
```

本次浏览器插件部署的现场回滚点：

```text
/home/fox/WeKnora/backups/frontend-dist-before-01856aeb-20260818-011314
/home/fox/WeKnora/.env.before-extension-v110-20260818-011314
/home/fox/WeKnora/backups/frontend-dist-before-36c901fb-20260818-021956
/home/fox/WeKnora/.env.before-extension-v120-20260818-021956
/home/fox/WeKnora/.env.before-embedded-images-20260818-023025
/home/fox/WeKnora/backups/frontend-dist-before-eb02a753-20260818-090539
/home/fox/WeKnora/.env.before-edge-zip-20260818-090539
/home/fox/WeKnora/.env.before-ssrf-20260818-111625
/home/fox/WeKnora/backups/frontend-dist-before-deaa8ed7-20260818-133932
/home/fox/WeKnora/.env.before-extension-v130-20260818-133932
/home/fox/WeKnora/backups/frontend-dist-before-5b492baa-20260818-175734
/home/fox/WeKnora/backups/frontend-dist-live-before-5b492baa-20260818-175734
/home/fox/WeKnora/.env.before-extension-v131-20260818-175734
```

### 5.2 模型采用状态

当前模型采用前状态已保存：

```text
/home/fox/WeKnora/backups/models-before-company-adoption-20260817-2336.csv
```

后续变更前可重复生成：

```bash
docker exec WeKnora-postgres sh -lc \
  'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -F "|" -Atc \
  "SELECT id, tenant_id, is_builtin, managed_by
   FROM models WHERE deleted_at IS NULL ORDER BY id;"' \
  > "backups/models-before-deploy-${DEPLOY_TS}.csv"
```

### 5.3 当前镜像

切换前记录镜像 ID，并为关键镜像增加回滚标签：

```bash
docker inspect WeKnora-app WeKnora-frontend WeKnora-docreader \
  --format '{{.Name}} {{.Image}} {{.Config.Image}}'

docker image tag "$(docker inspect WeKnora-app --format '{{.Image}}')" \
  "weknora-backup-app:${DEPLOY_TS}"
```

如 Docker 已清理某个运行容器引用的旧镜像对象，应保留现有可用标签和源码备份，不得假定容器显示的 SHA 仍可被 `docker image tag` 读取。

## 6. 拉取代码

```bash
cd /home/fox/WeKnora
git status --short --branch

HTTP_PROXY=http://100.70.139.102:7890 \
HTTPS_PROXY=http://100.70.139.102:7890 \
git fetch origin codex/jiwai-branding

HTTP_PROXY=http://100.70.139.102:7890 \
HTTPS_PROXY=http://100.70.139.102:7890 \
git pull --ff-only

git rev-parse HEAD
git rev-parse origin/codex/jiwai-branding
```

禁止在生产目录执行 `git reset --hard`、`git clean -fdx` 或覆盖 `.env`。

## 7. 构建镜像

### 7.1 构建变量

```bash
cd /home/fox/WeKnora

DEPLOY_COMMIT=$(git rev-parse HEAD)
DEPLOY_SHORT=$(git rev-parse --short=8 HEAD)
DEPLOY_TAG="deploy-${DEPLOY_SHORT}"
BUILD_TIME=$(date --iso-8601=seconds)
PROXY_URL=http://100.70.139.102:7890
```

不要复用 `HOME`、`CODEX_HOME` 等系统变量保存部署值。

### 7.2 app

```bash
docker build --progress=plain \
  --build-arg VERSION_ARG=v0.7.2 \
  --build-arg COMMIT_ID_ARG="$DEPLOY_COMMIT" \
  --build-arg BUILD_TIME_ARG="$BUILD_TIME" \
  --build-arg GO_VERSION_ARG=1.26 \
  --build-arg GOPROXY_ARG=https://proxy.golang.org,direct \
  --build-arg GOSUMDB_ARG=off \
  --build-arg APK_MIRROR_ARG=mirrors.aliyun.com \
  --build-arg WITH_ANYDOC=1 \
  --build-arg RUSTUP_INIT_URL=https://rsproxy.cn/rustup-init.sh \
  --build-arg RUSTUP_DIST_SERVER=https://rsproxy.cn \
  --build-arg RUSTUP_UPDATE_ROOT=https://rsproxy.cn/rustup \
  --build-arg HTTP_PROXY="$PROXY_URL" \
  --build-arg HTTPS_PROXY="$PROXY_URL" \
  --build-arg NO_PROXY=mirrors.aliyun.com,rsproxy.cn,localhost,127.0.0.1 \
  --build-arg http_proxy="$PROXY_URL" \
  --build-arg https_proxy="$PROXY_URL" \
  --build-arg no_proxy=mirrors.aliyun.com,rsproxy.cn,localhost,127.0.0.1 \
  -f docker/Dockerfile.app \
  -t "wechatopenai/weknora-app:${DEPLOY_TAG}" .
```

成功门槛：

- rustup 从 RSProxy 安装成功。
- anydoc 静态库生成成功。
- Go 主程序编译成功。
- 最终镜像导出成功。

### 7.3 frontend

远端宿主机不安装 Node，使用一次性 Node 22 容器：

```bash
docker run --rm \
  --user "$(id -u):$(id -g)" \
  -e HOME=/tmp/node-home \
  -e HTTP_PROXY="$PROXY_URL" \
  -e HTTPS_PROXY="$PROXY_URL" \
  -e npm_config_proxy="$PROXY_URL" \
  -e npm_config_https_proxy="$PROXY_URL" \
  -e VITE_FRONTEND_COMMIT="$DEPLOY_SHORT" \
  -e VITE_IS_DOCKER=true \
  -v "$PWD":/src \
  -w /src/frontend \
  node:22-bookworm-slim \
  sh -c 'npm ci && npm run build'

docker build --progress=plain \
  -t "wechatopenai/weknora-ui:${DEPLOY_TAG}" frontend
```

构建后删除 `frontend/node_modules`，保留 `frontend/dist` 供镜像封装：

```bash
docker run --rm -v "$PWD":/src node:22-bookworm-slim \
  rm -rf /src/frontend/node_modules
```

### 7.4 docreader

docreader 源码未变化时可把当前健康镜像增加新标签，不重复构建：

```bash
docker image tag \
  "$(docker inspect WeKnora-docreader --format '{{.Image}}')" \
  "wechatopenai/weknora-docreader:${DEPLOY_TAG}"
```

只有 docreader 源码、依赖或镜像基础层变化时才执行完整构建。

## 8. 更新版本号并切换

### 8.1 安全更新 `.env`

```bash
ENV_TMP=$(mktemp ./.env.deploy.XXXXXX)
awk -v tag="$DEPLOY_TAG" '
  BEGIN { updated = 0 }
  /^WEKNORA_VERSION=/ {
    print "WEKNORA_VERSION=" tag
    updated = 1
    next
  }
  { print }
  END {
    if (!updated) print "WEKNORA_VERSION=" tag
  }
' .env > "$ENV_TMP"
chmod 600 "$ENV_TMP"
mv "$ENV_TMP" .env
```

### 8.2 切换 app

```bash
AUTO_MIGRATE=true WEKNORA_VERSION="$DEPLOY_TAG" \
docker compose -p weknora up -d --no-deps app

docker inspect WeKnora-app \
  --format 'image={{.Config.Image}} state={{.State.Status}} restarts={{.RestartCount}} health={{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}'

docker logs --since 2m WeKnora-app 2>&1 | tail -n 200
```

必须确认 app `healthy`、重启次数为 0、日志无 `panic`、`FATAL`、`ERROR` 后再继续。

### 8.3 切换 frontend 和 docreader

仅在对应源码或运行配置变化时重建容器：

```bash
docker compose -p weknora up -d --no-deps frontend
docker compose -p weknora up -d --no-deps docreader
```

不要为了标签显示而重启镜像 ID 未变化且健康的服务。

## 9. 当前部署专用配置

### 9.1 公司预置模型

配置文件：

```text
config/builtin_models.yaml
```

Compose 只读挂载：

```yaml
- ./config/builtin_models.yaml:/app/config/builtin_models.yaml:ro
```

当前采用 8 个已配置模型。加载器仅设置 `is_builtin=true`，保留 `tenant_id`、`managed_by`、默认状态、参数、扩展配置和加密凭据。

启动成功日志：

```text
[builtin-models] applied: 0 upserted, 8 adopted, 0 pruned
```

当前新空间 `10002` 的实际可见性查询结果为：

```text
14 total / 8 builtin
```

### 9.2 平台解析引擎

迁移文件：

```text
migrations/versioned/000085_platform_parser_engine_config.up.sql
```

当前数据库状态：

```text
schema_migrations: 85, dirty=false
platform_parser_engine_configs: 1 row
```

迁移会在历史空间解析配置唯一时回填到平台配置；存在多份不一致配置时主动阻断，禁止静默覆盖凭据。

### 9.3 MinIO SSRF 白名单

Docker 内部 MinIO 使用私网地址，`SSRF_WHITELIST_EXTRA` 必须包含 `minio`。缺失时 app 会在启动阶段报错：

```text
unsafe MinIO endpoint: SSRF validation failed
```

当前公司模型服务 `http://192.168.0.20:8976/v1` 已按精确 IP 放行。app 容器中的 `SSRF_WHITELIST_EXTRA` 已确认包含 `192.168.0.20`，容器到 `/v1/models` 返回 HTTP 401，说明网络连通且目标接口要求认证。

只允许可信的内部服务名或精确 IP 进入白名单，不要为单个服务放开整个私网 CIDR。

## 10. 验收

### 10.1 容器与健康检查

```bash
docker compose -p weknora ps
curl -fsS http://127.0.0.1:8080/health
curl -fsSI http://127.0.0.1:8081/ | head
```

期望：

- app、docreader 为 healthy。
- frontend 为 running，首页 HTTP 200。
- app、frontend、docreader 重启次数均为 0。

### 10.2 静态资源

```bash
curl -fsSI http://127.0.0.1:8081/downloads/jiwai-knowledge-skill.zip | head
curl -fsSI http://127.0.0.1:8081/downloads/jiwai-knowledge-assistant-1.3.1.crx | head
curl -fsSI http://127.0.0.1:8081/downloads/jiwai-knowledge-assistant-1.3.1.zip | head
```

当前基线：

- Skill ZIP：HTTP 200，9416 字节，SHA256 为 `9ca6bffdb421ff3f9b231026ae3bb7d6d6265a62ebe7eb34a72a45621dad9a0f`。
- 见外知识库助手 CRX：HTTP 200，297911 字节，SHA256 为 `a8cc4d76a4f92c0ba720934942730a935f67184cf4e789a173f83e217c1c68cc`。
- 见外知识库助手 ZIP：HTTP 200，296014 字节，SHA256 为 `d8b6f6bfe1cda46ead943c6982f8ae83fb5e26ca025e4ae1049e1dc5c18e5d68`。
- ZIP 的响应类型为 `application/zip`；CRX 的响应类型为 `application/x-chrome-extension`。
- `v1.3.0` 安装包继续保留作为直接回滚资源；`v1.2.0`、`1.1.0` 和 `1.0.0` 下载路径仍保留兼容。

Skill ZIP 已从线上地址重新下载，并使用官方 PowerShell 容器执行 AST 解析，结果为 `DEPLOYED_POWERSHELL_PARSE_OK`。
浏览器插件 ZIP 已从线上地址重新下载，线上与源码包 SHA256 一致；解析 Manifest 后确认品牌名、`v1.3.1`、`all_frames=true`、`unlimitedStorage=true`，并包含 `collection.js`。线上设置页构建产物只引用 `jiwai-knowledge-assistant-1.3.1.zip`。

Edge 普通用户安装步骤：

1. 删除被停用或提示来源未知的旧扩展。
2. 下载 `jiwai-knowledge-assistant-1.3.1.zip`，解压到不会移动或删除的固定目录。
3. 打开 `edge://extensions`，开启开发人员模式。
4. 选择「加载解压缩的扩展」，选中直接包含 `manifest.json` 的目录。
5. 启用插件并填写 API Key。

直接从网页下载并拖入自签名 CRX 时，新版 Edge 可能提示 `CRX_REQUIRED_PROOF_MISSING`。该错误属于安装来源信任校验，不能通过重新生成同类自签名 CRX 解决。公司统一安装应发布到 Edge 扩展商店，或配置扩展安装来源、允许列表和强制安装策略；CRX 响应类型正确只是企业策略分发的必要条件之一。

官方参考：[本地加载扩展](https://learn.microsoft.com/en-us/microsoft-edge/extensions/getting-started/extension-sideloading)、[其它分发方式](https://learn.microsoft.com/en-us/microsoft-edge/extensions/developer-guide/alternate-distribution-options)、[企业扩展管理](https://learn.microsoft.com/en-us/deployedge/microsoft-edge-manage-extensions-webstore)。

浏览器运行时验收已通过 ego 完成：巨量引擎长文档 `content/140962` 的编辑器保存前包含 22 个标题、95 行 Markdown 表格、47 张图片和尾部附录；可编辑预览保留标题、Callout、表格和图片。

`v1.3.1` 针对巨量引擎 `content/147621` 完成真实页面验收：旧逻辑只能得到 149 字符外壳；修复后约 25.9 秒完成 88 个内容块、7182 字符、27 个标题和 32 行 Markdown 表格，首段正文与尾部“在哪可以查看用户具体的搜索词”均存在。附件被清理为 `0731-线索行业搜索首位广告白皮书 .pdf`，保留来源站实际暴露的 1 张远程 PDF 封面，加载失败 SVG 占位图为 0。该查看器没有向页面暴露更多可访问原图时，插件会保留未加载说明，不伪造图片或把大段 SVG data URI 写入知识库。

`v1.3.0` 目录发现运行验收：巨量帮助中心识别 59 篇并按上限返回 50 篇；NatureTunnel 文档分类页递归发现 17 篇，OAuth 文档提取 24942 字符和 106 行 Markdown 表格；抖音生活服务首页识别 28 个入口，商家分类页发现 20 篇课程。未登录课程页会识别“当前页面需要登录查看”并暂停任务。

长网页截取隔离验收：初始选区为 600×380，移动后位置变化且保留 8 个缩放手柄，最终拼接生成 600×3101 长图并进入编辑器；保存载荷包含正文和截图。后台状态机测试覆盖 URL 去重、递归追加、暂停、继续、逐页完成、关闭任务标签页和截图随 Markdown 入库。

使用具备写权限的 Key 入库后，知识 `07250a70-a82d-4c06-9f28-5e0a8b219651` 达到 `parse_status=completed`、`enable_status=enabled`。metadata 为 16349 字符，保留 95 行表格和尾部附录，包含 47 个 `resource://` 图片引用且无 data URI 残留。该条目暂保留在“星苒”供人工查看排版。

### 10.3 数据库

```bash
docker exec WeKnora-postgres sh -lc \
  'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc \
  "SELECT version, dirty FROM schema_migrations;
   SELECT COUNT(*) FROM platform_parser_engine_configs;
   SELECT COUNT(*), COUNT(*) FILTER (WHERE is_builtin = true)
   FROM models
   WHERE deleted_at IS NULL AND (tenant_id = 10002 OR is_builtin = true);"'
```

期望输出：

```text
85|f
1
14|8
```

### 10.4 页面与 API

必须使用真实浏览器登录后检查：

- 见外知识库 Skill 页面可见平台标签、API Key 脱敏预览、下载按钮和安全提示。
- Chrome 插件页默认下载 ZIP，页面明确提示解压后通过「加载解压缩的扩展」安装。
- 解析引擎页显示 9 个公司预置引擎，DocReader 已连接。
- 模型页显示 8 个公司预置模型。
- 普通用户响应不得包含公司模型凭据、Base URL 或扩展配置。
- `/api/v1/system/info` 返回当前提交号且 `db_migration_error` 为空。

### 10.5 端到端解析

每次大版本部署至少完成一次可清理的真实闭环：

1. 向测试知识库上传唯一命名文本文件。
2. 轮询到 `parse_status=completed`、`enable_status=enabled`。
3. 删除测试知识。
4. 创建临时会话并上传聊天附件。
5. 轮询附件到 `status=ready`，确认 token 和 chunk 数量大于 0。
6. 删除附件和临时会话。
7. 直接查询数据库确认没有活动验收记录残留。

当前版本已验证：知识解析完成并启用；临时附件为 23 tokens、1 chunk；测试数据全部清理。

## 11. 回滚

### 11.1 回滚 app 镜像

```bash
cp -a .env ".env.failed-$(date +%Y%m%d-%H%M%S)"

WEKNORA_VERSION=deploy-2be2572 \
docker compose -p weknora up -d --no-deps app
```

若回滚镜像不包含当前数据库迁移，应临时传入 `AUTO_MIGRATE=false`，待确认迁移兼容后再恢复。

### 11.2 回滚公司模型采用

本次采用前 8 个公司模型均为 `is_builtin=false`。需要撤销时必须在事务中按 `config/builtin_models.yaml` 的 8 个 ID 明确更新，禁止按整个租户无条件批量操作：

```sql
BEGIN;

UPDATE models
SET is_builtin = false
WHERE id IN (
  'f7151059-adf6-449f-b90c-c831e63f9107',
  '76c28a52-783d-453a-b86c-9870ddb1cb80',
  'faec5372-e975-4b77-b1d4-9c297c8a49a0',
  '13fc4cb3-30d7-42e6-81d2-60029d4060a1',
  '9a3875c5-9db5-4dbb-a612-1104d7807af1',
  '34b1ce37-049a-4c8c-ae0e-fabfce887f5d',
  '94e07843-7fbb-40b2-8e5b-36ff720206e1',
  'b7691a7d-4406-44e6-9bb0-c1352f7982fc'
);

COMMIT;
```

执行前后都应与 `models-before-company-adoption-20260817-2336.csv` 对照。

### 11.3 回滚源码目录

只有当前 Git 仓库损坏且无法通过正常 Git 操作恢复时，才使用：

```text
/home/fox/WeKnora-backup-20260817-215603
```

目录切换前必须停止将要重建的核心容器，并保留失败目录，不得直接删除。

## 12. 已知非阻塞项

- 前端依赖安装曾报告 2 个 moderate、6 个 high npm audit 告警；本次未做无关依赖升级。
- frontend 未定义 Docker healthcheck，必须额外做 HTTP 200 和浏览器渲染验收。
- 构建缓存不能放在仓库根目录 `.cache`，否则会被纳入 app build context，造成数 GB 无效上下文。
- Docker daemon 拉基础镜像不一定继承命令行代理；已有基础镜像应保留，网络异常时先区分 daemon 拉取与容器内下载。
- 不要把 `config/builtin_models.yaml` 替换为含明文凭据的完整模型配置；当前采用清单专门用于复用数据库内已有安全配置。

## 13. 最终交付检查表

- [ ] 本地工作树已审核并提交。
- [ ] origin 分支与本地 HEAD 一致。
- [ ] 远端 Git 工作树干净且 HEAD 正确。
- [ ] `.env`、模型状态和旧镜像已备份。
- [ ] app 镜像包含 anydoc、迁移、Skill 和当前提交号。
- [ ] app 先切换并恢复 healthy。
- [ ] frontend、docreader 仅在实际变化时切换。
- [ ] 迁移版本 clean，平台解析配置存在。
- [ ] 公司预置模型数量正确且普通用户脱敏。
- [ ] Skill ZIP、Chrome CRX 可下载。
- [ ] 知识库和临时附件端到端解析通过。
- [ ] 验收数据已清理。
- [ ] 日志无 panic、FATAL、ERROR。
- [ ] 回滚路径和备份文件可读。
