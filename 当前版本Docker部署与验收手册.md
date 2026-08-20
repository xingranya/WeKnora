# WeKnora 当前版本 Docker 部署与验收手册

最后现场核验：2026-08-20 11:26（Asia/Shanghai）

本文记录见外传媒当前 WeKnora 分支在远端生产机上的 Docker 部署方式、配置边界、验收门槛和回滚方法。所有命令均不得包含 SSH 密码、API Key、数据库密码或模型凭据。

## 1. 当前生产基线

| 项目 | 当前值 |
| --- | --- |
| 本地仓库 | `/Users/xingranya/Downloads/GitHub-clone/WeKnora` |
| 远端主机 | `fox@100.78.64.62` |
| 远端仓库 | `/home/fox/WeKnora` |
| Git 远端 | `https://github.com/xingranya/WeKnora.git` |
| 生产分支 | `codex/jiwai-branding` |
| 生产功能提交 | `80e66d563d5e165b24677fb0d7b2364d687614e4` |
| 最新前端修复提交 | `acb62eb66b0057432a7d5142ca3ee9310521dca2` |
| MCP 测试门禁提交 | `2c7616c38b3e20a63bbb289048e53fd79ce0bbbc` |
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
| app | `wechatopenai/weknora-app:deploy-acb62eb6` | `deploy-80e66d56`，镜像 ID `sha256:d3e0f56e0331a4760c7d44b2e555f7dd2b19b57d7ec3f63c32d9959cb782af0f` | healthy，重启 0；仅建立新版本别名，未重启 |
| frontend | `wechatopenai/weknora-ui:deploy-acb62eb6` | `deploy-acb62eb6`，镜像 ID `sha256:a9563ac54c6a36af4414e83676dcd8eaf0dea02b668ef8aeeb86eb9184f96f07` | running，重启 0 |
| docreader | `wechatopenai/weknora-docreader:deploy-acb62eb6` | `deploy-ddf88efa`，镜像 ID `sha256:b9c4636b65b5d4947d5e09cd311ba6cf37f1f2da37c51d4be2b911d432f12abe` | healthy，重启 0；仅建立新版本别名，未重启 |

`a9a09aab` 包含知识库目录一致性、问题生成并发与索引补偿等后端修复；`c403766b` 是其后的前端验收修复，解决上传确认层关闭顺序、并发确认请求隔离、过期标签响应、纯文本消息空图片占位和 Wiki 图片预览键盘焦点；`80e66d56` 进一步让知识库与聊天临时附件共享 `parser:mineru` 分布式并发租约；`acb62eb6` 将上传队列入口限制到知识库文档详情页。当前仅重建并切换 frontend，app/docreader 继续复用健康镜像并增加 `deploy-acb62eb6` 别名。

### 1.2 当前关键环境开关

远端 `.env` 已现场确认以下非敏感项：

```dotenv
WEKNORA_VERSION=deploy-acb62eb6
AUTO_MIGRATE=false
STORAGE_TYPE=minio
SSRF_WHITELIST_EXTRA=searxng,qdrant,milvus,weaviate,doris-fe,doris-be,host.docker.internal,minio,192.168.0.20
KNOWLEDGE_UPLOAD_MAX_FILE_SIZE_MB=2048
KNOWLEDGE_UPLOAD_CHUNK_SIZE_MB=4
KNOWLEDGE_UPLOAD_SESSION_TTL_HOURS=24
KNOWLEDGE_UPLOAD_TEMP_DIR=/data/files/upload-sessions
```

`KNOWLEDGE_UPLOAD_*` 是知识库可续传上传的非敏感部署参数：默认单文件 2 GiB、单分片 4 MiB、会话保留 24 小时，暂存目录位于 app 的持久化 `/data/files` 卷内。其余 `.env` 内容包含数据库、Redis、MinIO、JWT、模型和集成凭据，不得写入 Git、命令参数、构建日志或本手册。

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
- 持久化知识库文件夹、可续传上传会话及迁移 `000086`。
- 知识库、临时文档、聊天附件统一使用平台解析配置。
- 模型设置、解析引擎设置、API、国际化和回归测试更新。
- 问题生成单批最多 4 个模型调用并发，带 operation/revision fencing、持久补偿、Housekeeping 恢复和父子分块共享锁域。
- 目录切换使用知识库、目录路径和请求代次三重提交保护；新建目录乐观显示，迟到响应不能覆盖当前目录。
- 上传确认在队列启动前进入关闭状态，并用 request revision 隔离并发操作及异步标签结果；全局队列跨页面持续运行。
- Docker 构建支持 Debian、Rust 镜像参数。

关键提交：

```text
80e66d5 fix(parser): 统一临时附件 MinerU 并发
c403766 fix(upload): 隔离过期标签响应
4cc6ccc fix(wiki): 保留图片预览键盘交互
7d00046 fix(upload): 取消被替代的确认请求
fb81d6b fix(chat): 隐藏空图片预览
14341e7 fix(upload): 确认后立即关闭上传弹窗
a9a09aa fix(knowledge): 修复目录切换与创建反馈
bbafa09 fix(knowledge): 保证问题生成与索引一致性
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
6. 构建成功前不切换运行容器；正式切换顺序固定为 `migration -> docreader -> app -> frontend`。其中 `migration` 使用当前 app 镜像内的 migrate 工具执行，完成后 app 使用 `AUTO_MIGRATE=false` 启动。
7. `000086` 迁移必须 clean，docreader 必须先恢复 healthy，之后才能切换 app；app 未恢复 healthy 前不得继续切换 frontend。
8. 任何模型采用、迁移和清理操作必须先生成可恢复备份；`000086` 的 down 迁移会删除文件夹和上传会话表，不能把它当作无损回滚。

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

本次知识库文件夹与可续传上传发布的完整回滚快照：

```text
/home/fox/WeKnora/backups/releases/20260819-154754
```

该目录约 13 GiB，包含 16 个运行镜像归档、10 个 `weknora_*` Docker 卷、PostgreSQL custom dump 与 globals、`.env`/Compose/config/frontend dist、Git bundle、MinerU systemd 配置和可恢复 Conda 环境。镜像归档为 `images/running-images-20260819-154754.tar.zst`，三个核心镜像另有不可覆盖回滚标签 `rollback-before-large-upload-20260819-154754`。回滚时先停止写入并按清单选择代码、镜像、数据库或卷恢复；不要直接覆盖运行中的数据卷，也不要把快照中的任何环境文件内容复制到命令、日志或文档中。

问题生成、目录一致性、前端收尾与解析 gate 另有三级发布快照：

```text
/home/fox/WeKnora/backups/releases/20260820-053502-question-folder-fix
/home/fox/WeKnora/backups/releases/20260820-063053-pre-ui-final
/home/fox/WeKnora/backups/releases/20260820-075247-pre-parser-gate
```

`20260820-053502-question-folder-fix` 保存当时的 `.env`、Compose、容器配置、源码 bundle 以及 app/frontend 镜像归档；`source.bundle` SHA256 为 `618599de575c71dcda910ddb1f2289be931368e630abeddfb2d6cc48815cc4c4`，`images/app-frontend.tar.zst` SHA256 为 `520ec1cfc83728b4953d6d4a556426e9fbc354293200563459395c71125bd8fb`。

`20260820-063053-pre-ui-final` 是切换 `c403766b` frontend 前的直接回滚点，约 2.2 GiB，保存 `.env`、Compose、容器配置、源码 bundle 和当时正在运行的 app/frontend/docreader 三个核心镜像。`source.bundle` SHA256 为 `b4c3e82249a40285723e0271136a01538dbf8f2f19acb943fc4ca5ba9021a3ae`，`images/core-images.tar.zst` SHA256 为 `1cf208bf9a5414eec3d8871958664ced0d6f525b555327250b06f08b8112b74b`；Git bundle 和 zstd 完整性均已验证。三个旧镜像带有 `rollback-before-ui-final-20260820-063053` 不可覆盖标签。

`20260820-075247-pre-parser-gate` 是 app `80e66d56` 切换前的直接回滚点，保存 `.env`、Compose、容器配置，并给切换前的 app/frontend/docreader 镜像增加 `rollback-before-parser-gate-20260820-075247` 标签；该快照用于仅回滚 app 并保留已验收 frontend/docreader。

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

### 8.2 执行 migration

当前 Compose 没有独立的 migration 服务；迁移工具随 app 镜像放在 `/usr/local/bin/migrate`，迁移文件位于 `/app/migrations/versioned`。先使用新 app 镜像执行迁移，再按 `docreader -> app -> frontend` 切换运行服务。下面的命令只引用 Compose 已加载的环境变量，不在命令行填写或输出任何数据库凭据。

`000086` 不会改写历史 `knowledges.folder_path`。它会在创建新表前只读预检可确定的结构边界；如果报告历史路径需要应用层规范化，必须立即停止，先备份并审计对应知识记录，禁止用临时 SQL 批量改写原路径后强行继续迁移。

```bash
WEKNORA_VERSION="$DEPLOY_TAG" \
docker compose -p weknora run --rm --no-deps \
  --entrypoint /bin/sh app \
  -lc 'DB_URL=$(python3 -c "import os, urllib.parse as u; e=os.environ; print(\"postgres://{}:{}@{}:{}/{}?sslmode=disable\".format(u.quote(e[\"DB_USER\"], safe=\"\"), u.quote(e[\"DB_PASSWORD\"], safe=\"\"), e[\"DB_HOST\"], e[\"DB_PORT\"], e[\"DB_NAME\"]))"); exec migrate -path /app/migrations/versioned -database "$DB_URL" up'
```

迁移命令返回成功后，必须确认 `000086_knowledge_folders_and_upload_sessions` 已应用且状态 clean：

```bash
docker exec WeKnora-postgres sh -lc \
  'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc \
  "SELECT version, dirty FROM schema_migrations;"'
```

期望最后一行类似 `86|f`。如果迁移失败、版本为 dirty 或未达到 86，立即停止，不得继续切换服务。

### 8.3 切换 docreader

```bash
WEKNORA_VERSION="$DEPLOY_TAG" \
docker compose -p weknora up -d --no-deps docreader

docker inspect WeKnora-docreader \
  --format 'image={{.Config.Image}} state={{.State.Status}} restarts={{.RestartCount}} health={{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}'

docker logs --since 2m WeKnora-docreader 2>&1 | tail -n 200
```

必须确认 docreader `healthy`、重启次数为 0，且日志无 `panic`、`FATAL`、`ERROR` 后再继续。

### 8.4 切换 app

```bash
AUTO_MIGRATE=false WEKNORA_VERSION="$DEPLOY_TAG" \
docker compose -p weknora up -d --no-deps app

docker inspect WeKnora-app \
  --format 'image={{.Config.Image}} state={{.State.Status}} restarts={{.RestartCount}} health={{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}'

docker logs --since 2m WeKnora-app 2>&1 | tail -n 200
```

必须确认 app `healthy`、重启次数为 0、日志无 `panic`、`FATAL`、`ERROR` 后再继续。

### 8.5 切换 frontend

仅在对应源码或运行配置变化时切换 frontend：

```bash
docker compose -p weknora up -d --no-deps frontend
```

切换完成后的服务顺序必须保持为 migration 已完成、docreader healthy、app healthy、frontend HTTP 200。不要使用普通的 `docker compose up -d` 让 Compose 依据 `depends_on` 自行重排发布顺序。

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
schema_migrations: 86, dirty=false
platform_parser_engine_configs: 1 row
```

迁移会在历史空间解析配置唯一时回填到平台配置；存在多份不一致配置时主动阻断，禁止静默覆盖凭据。

生产自部署 MinerU 运行在宿主机 systemd 服务中，不属于 Docker Compose：

```text
服务：mineru-api.service
版本：MinerU 3.4.5
环境：/home/fox/miniconda3/envs/mineru-3.4.5
端口：0.0.0.0:8000
模式：pipeline，CUDA_VISIBLE_DEVICES=-1
并发：MINERU_API_MAX_CONCURRENT_REQUESTS=2
```

2026-08-19 已从 MinerU 2.7.0 全量替换为 3.4.5。旧版只接受 PDF 和图片，虽然 WeKnora 将 `docx` 路由到 MinerU，服务仍会返回 `Unsupported file type: docx`。3.4.5 的上传白名单已包含 `docx`、`pptx` 和 `xlsx`；现场从 WeKnora app 容器调用生产 `http://host.docker.internal:8000/file_parse`，DOCX 返回 HTTP 200、`version=3.4.5`，标题、正文和表格均存在。

当前 systemd 与平台解析配置的 `mineru_max_concurrency` 均为 2。WeKnora 以解析引擎为 key 使用 Redis 分布式信号量，知识库和聊天临时附件共用 `parser:mineru` 租约；超过上限的任务进入等待状态，不直接把并发压力打到 MinerU，Lite 模式使用本地信号量。服务器虽有 GTX 1060 6GB，但当前生产明确使用 CPU pipeline，不应把显卡存在本身当作提高并发的依据。继续提高并发前必须重新覆盖大文件、扫描 PDF、Office 文档、RSS、swap、p95 和 30 分钟浸泡测试。

旧 `/home/fox/miniconda3/envs/mineru` 环境已按要求删除，仅保留新环境。升级前配置和依赖清单位于：

```text
/home/fox/backups/mineru-before-3.4.5-20260819-134615
```

### 9.3 MinIO SSRF 白名单

Docker 内部 MinIO 使用私网地址，`SSRF_WHITELIST_EXTRA` 必须包含 `minio`。缺失时 app 会在启动阶段报错：

```text
unsafe MinIO endpoint: SSRF validation failed
```

当前公司模型服务 `http://192.168.0.20:8976/v1` 已按精确 IP 放行。app 容器中的 `SSRF_WHITELIST_EXTRA` 已确认包含 `192.168.0.20`，容器到 `/v1/models` 返回 HTTP 401，说明网络连通且目标接口要求认证。

只允许可信的内部服务名或精确 IP 进入白名单，不要为单个服务放开整个私网 CIDR。

### 9.4 持久化文件夹与可续传上传

本次版本由 PostgreSQL 迁移 `migrations/versioned/000086_knowledge_folders_and_upload_sessions.up.sql` 创建持久化文件夹、上传会话和上传分片表，并回填已有知识条目的文件夹祖先目录；生产当前状态已现场确认是 `86|f`。SQLite/Lite 使用 `migrations/sqlite/000005_knowledge_folders_and_uploads` 提供同等表和字段。上传会话保存 `final_file_path` 和 `finalize_stage`，用于服务重启后继续完成或清理已经写入最终存储的对象；`knowledges.source_file_quota_bytes` 专用列记录已计入空间配额的原始文件字节数。对应 API 位于 `/api/v1/knowledge-bases/:id/knowledge/folders` 和 `/api/v1/knowledge-bases/:id/knowledge/uploads`。

当前非敏感配置为：

```text
KNOWLEDGE_UPLOAD_MAX_FILE_SIZE_MB=2048       # 2 GiB
KNOWLEDGE_UPLOAD_CHUNK_SIZE_MB=4             # 4 MiB
KNOWLEDGE_UPLOAD_SESSION_TTL_HOURS=24        # 24 小时
KNOWLEDGE_UPLOAD_TEMP_DIR=/data/files/upload-sessions
```

`KNOWLEDGE_UPLOAD_CHUNK_SIZE_MB` 只允许 1-16 的整数。当前 Compose 只持久化 `/data/files`，所以 `KNOWLEDGE_UPLOAD_TEMP_DIR` 必须位于该目录下；多 app 副本必须把同一绝对路径挂载为所有副本可读写的共享 RWX 存储。前端队列默认只运行一个活动上传任务，其他任务排队执行；当前已取消人为上传带宽限速。该单活动策略是用户界面队列的默认调度方式，不是新增的带宽限制。服务端同一用户最多保留 5 个未完成会话，同一空间最多保留 10 GiB 未完成上传暂存量；不能据此扩大并发或绕过会话过期清理。

app 启动后立即扫描 `completing` 和 cleanup-pending 会话，之后每 5 分钟重试；恢复时必须加载会话所属空间的完整存储配置，不能回退到其他空间或进程默认后端。最终对象流程为“持久化物理准备路径 -> 流式写入 -> 幂等注册资源引用 -> 创建知识 -> 清理分片”，任一步骤重启后都从数据库阶段继续。

### 9.5 空凭据即时通讯渠道

2026-08-20 发布时发现公司空间 `10000` 有一个已启用但凭据为空的飞书 WebSocket 渠道，启动日志报 `appSecret and clientAssertionProvider cannot be nil`。现场先将渠道 ID、空间、平台、名称、启用状态、模式和更新时间保存到 `20260820-053502-question-folder-fix/im-channel-before-disable.txt`，再用“仍启用且 credentials 仍为空”作为条件只禁用这一条记录，更新 1 行。

禁用后 app 启动日志为 `Loaded 0 enabled channels`，当前日志无 panic、FATAL、ERROR。恢复该渠道前必须先通过管理界面配置有效凭据并完成连接测试，再重新启用；不得把凭据写入本手册、SQL、命令行或 Git。

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

2026-08-20 现场结果：app 为 `deploy-80e66d56` 且 healthy，frontend 为 `deploy-c403766b`，docreader 为 `deploy-ddf88efa` 且 healthy，三者重启次数均为 0；app `/health`、公网 `https://know.seeway.co/` 均返回 HTTP 200。app 容器已确认 `AUTO_MIGRATE=false`；frontend 镜像内 `Settings-*.js` 已确认包含前端提交 `c403766`。

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
   SELECT to_regclass('"'"'public.knowledge_folders'"'"');
   SELECT to_regclass('"'"'public.knowledge_upload_sessions'"'"');
   SELECT to_regclass('"'"'public.knowledge_upload_parts'"'"');
   SELECT character_maximum_length
   FROM information_schema.columns
   WHERE table_schema = '"'"'public'"'"'
     AND table_name = '"'"'knowledge_upload_sessions'"'"'
     AND column_name = '"'"'user_id'"'"';
   SELECT COUNT(*)
   FROM information_schema.columns
   WHERE table_schema = '"'"'public'"'"'
     AND table_name = '"'"'knowledge_upload_sessions'"'"'
     AND column_name IN ('"'"'final_file_path'"'"', '"'"'finalize_stage'"'"');
   SELECT COUNT(*)
   FROM information_schema.columns
   WHERE table_schema = '"'"'public'"'"'
     AND table_name = '"'"'knowledges'"'"'
     AND column_name = '"'"'source_file_quota_bytes'"'"';
   SELECT indexdef LIKE '"'"'%completing%'"'"'
      AND indexdef LIKE '"'"'%completed_cleanup_pending%'"'"'
      AND indexdef LIKE '"'"'%cancelled_cleanup_pending%'"'"'
      AND indexdef LIKE '"'"'%expired_cleanup_pending%'"'"'
   FROM pg_indexes
   WHERE schemaname = '"'"'public'"'"'
     AND indexname = '"'"'idx_upload_sessions_expiry'"'"';
   SELECT COUNT(*), COUNT(*) FILTER (WHERE is_builtin = true)
   FROM models
   WHERE deleted_at IS NULL AND (tenant_id = 10002 OR is_builtin = true);"'
```

期望输出：

```text
86|f
1
knowledge_folders
knowledge_upload_sessions
knowledge_upload_parts
512
2
1
t
14|8
```

2026-08-20 已通过真实浏览器会话调用生产 API：初始化 `acceptance-resume-c403.txt` 返回 HTTP 201 和 `chunk_size=4194304`；上传第 0 片返回 HTTP 200，查询得到 `received_bytes=4194304`、`received_parts=[0]`；取消返回 HTTP 200，后续状态为 `cancelled`。数据库复核该会话分片记录数为 0，app 容器内对应 4 MiB 临时文件不存在，说明请求内同步清理成功。只有清理失败并停留在 `*_cleanup_pending` 时，Housekeeping 才负责后续重试。

### 10.4 页面与 API

必须使用真实浏览器登录后检查：

- 见外知识库 Skill 页面可见平台标签、API Key 脱敏预览、下载按钮和安全提示。
- Chrome 插件页默认下载 ZIP，页面明确提示解压后通过「加载解压缩的扩展」安装。
- 解析引擎页显示 9 个公司预置引擎，DocReader 已连接。
- 模型页显示 8 个公司预置模型。
- 普通用户响应不得包含公司模型凭据、Base URL 或扩展配置。
- `/api/v1/system/info` 返回当前提交号且 `db_migration_error` 为空。
- `.env` 中 `LOG_FORMAT` 留空或使用包含 `%msg` 的模板，禁止写成 `json`，否则容器日志只会输出字面量 `json`。

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

### 10.6 2026-08-20 生产真实验收记录

代码与构建验收：

- 远端构建容器内 `go test ./... -count=1` 全仓 Go 测试通过。
- frontend `npm test` 最终为 433/433 通过，`npm run type-check` 通过，国际化审计 11/11 通过。
- 本地和远端生产构建均完成 6387 个模块转换；远端 nginx frontend 镜像构建成功。
- DocReader 按 CI 锁定 Python 3.10.18 完成源码编译检查，143 项 Python 测试通过；其中 12 项因仓库未提供对应大型 PDF、PPTX、XLSX 夹具按测试条件跳过。远端 Go 1.26 容器中的 gRPC client/proto 测试通过。
- MCP Server 在 Python 3.10、3.11、3.12、3.13 下分别完成 23 项 unittest 和 6 项 pytest，四个版本均为 29/29 通过；sdist、wheel 构建成功，wheel 已确认包含 `upload_paths.py`。提交 `2c7616c3` 已把原先未被 `unittest discover` 收集的 `tests/` pytest 用例加入 CI 发布门槛。
- `cargo audit` 扫描 168 个 Rust 依赖未发现漏洞；Linux AMD64 anydoc 0.1.9 静态库构建成功，`go vet -tags anydoc`、`go test -tags anydoc -count=1` 和 `go build -tags anydoc ./cmd/server` 均通过。
- CLI 在远端 Linux/Go 1.26 容器中完成 `go build ./...`、`go test -race -coverprofile`、`go vet ./...`、Skill wire 词汇检查和文档凭据检查，全部通过；语句覆盖率为 69.4%。
- 多轮独立子代理审查依次发现并修复问题生成补偿、父子锁、目录迟到响应、上传确认并发、图片预览焦点、过期标签响应和聊天附件 MinerU gate；最终复审结论为 P0 无、P1 无。
- MCP CI 补测修改另经独立子代理审查，结论为 P0/P1/P2/P3 均无；每个 Python 版本的两套测试无重复收集，build/publish 依赖关系不变。
- `acb62eb6` 上传队列入口修复经独立子代理审查，结论为 P0/P1 均无；队列 store、分片上传和解析轮询仍跨路由运行，只有入口按路由隐藏。

ego 生产浏览器验收使用专用知识库 `发布验收-b0ac7fde-20260819`：

- 目录 `验收目录/子目录/终审目录-20260820` 创建后立即出现在目录树并被选中；切换目录时旧卡片立即清空，迟到响应未覆盖当前目录。
- 上传 `issue_2634_vertical_merge.docx` 时队列立即出现，状态按 `上传中 -> 等待解析 -> 已完成` 推进；详情保留两张表格和 2 个片段，Q0101/BRCA1、Q0102/BRCA2、Q0103/MLH1、Q0104/APC、15 个工作日及纵向合并检测方法均正确。
- 首个片段生成 3 个辅助召回问题。Trace 总耗时 10.88 秒，其中问题批次 7.50 秒、摘要 5.10 秒，两项后处理并行执行；问题生成不再表现为逐问题串行等待。
- 启用公司预置视觉模型后上传 `test_text.png`，队列跨知识库到新对话路由持续运行并完成；摘要和片段识别出图片中的 `OCR` 文本。
- 基础问答“遗传性乳腺癌对应的项目编号和相关基因是什么？”返回 `Q0101` 和 `BRCA1`，检索完成并显示 3 篇引用。
- app `80e66d56` 发布后通过聊天附件上传同一 DOCX，页面显示已解析 1 个附件，附件问答再次返回 `Q0101` 和 `BRCA1`；测试会话随后删除。运行队列接口返回 `parser_limiter_available=true`。
- 发布后再次上传 `docreader/README.md`：确认后队列立即出现，确认层完成关闭动画后不再残留，文件解析到已完成。纯文本会话中 `.t-image__error` 和空图片 trigger 均为 0。
- 本轮新增的 MD、PNG、DOCX、临时文件夹和测试会话均已删除；清理后目录树恢复为根目录 2 个文档、`验收目录/子目录` 1 个文档。
- 可续传上传生产 API 已完成 4 MiB 分片初始化、SHA256 校验上传、确认字节查询和取消，结果分别为 HTTP 201/200/200，取消后状态为 `cancelled`。

服务与资源验收：

- PostgreSQL migration 为 `86|f`；MinerU 3.4.5 systemd active、`NRestarts=0`，systemd 与平台并发均为 2。
- Skill ZIP、`jiwai-knowledge-assistant-1.3.1.crx`、`jiwai-knowledge-assistant-1.3.1.zip` 公网下载均为 HTTP 200。
- 最近 20 分钟 app 日志无 panic、FATAL、ERROR；公网首页为 HTTP 200。

### 10.7 2026-08-20 11:26 上传队列入口修复验收

- 远端 Git、origin 和本地提交均为 `acb62eb6`；frontend 切换为 `sha256:a9563ac54c6a36af4414e83676dcd8eaf0dea02b668ef8aeeb86eb9184f96f07`，app/docreader 镜像 ID 保持不变。
- 进入知识库详情页 `ego验收-云端向量-20260820030141` 时，`button[aria-label="上传队列"]` 数量为 1；进入全局新对话页时数量为 0；返回知识库详情页恢复为 1。说明入口不再污染聊天页，同时队列状态仍由全局 store 保持。
- 新建知识库时明确选择公司预置 `qwen3向量化模型`（`Qwen/Qwen3-VL-Embedding-8B`），没有使用 `BGE-M3 本地向量模型`。上传 `docreader/README.md` 后 `parse_status=completed`、文档已启用，远程向量模型初始化和 4096 维向量检索均成功。
- 本次问答尝试未计为通过：SiliconFlow 的 `deepseek-ai/DeepSeek-V4-Flash` 查询理解请求记录 `context deadline exceeded`，约 60 秒后 SSE 断开，浏览器停留在“正在理解问题…”。该失败发生在远程模型提供方，不是上传队列路由修复引入；测试会话随后已删除。
- frontend-only 回滚资产为 `/home/fox/WeKnora/backups/releases/20260820-111816-upload-queue-visibility`，`frontend-image.tar.zst` SHA256 为 `929f6499b6bee88770a47b47e339b826597ed8bb72ead2d8de6f27009f721fb9`。

## 11. 回滚

### 11.1 使用发布前快照回滚代码与镜像

本次 frontend 发布的直接回滚快照为：

```text
/home/fox/WeKnora/backups/releases/20260820-111816-upload-queue-visibility
```

该快照保存切换前的 frontend 镜像、容器配置和 `.env`，`frontend-image.tar.zst` 已完成 SHA256 校验。只回滚 `acb62eb6` 前端时，加载该归档并把旧 frontend 重标记到回滚版本；app 和 docreader 使用当前运行镜像 ID 建立同版本标签，避免全局 `WEKNORA_VERSION` 让后续 Compose 收敛意外撤销后端修复。数据库和卷保持不动。上一个完整 frontend 快照仍保留在 `/home/fox/WeKnora/backups/releases/20260820-063053-pre-ui-final`，全量灾难恢复仍使用 `/home/fox/WeKnora/backups/releases/20260819-154754`。

回滚前先停止将要重建的服务，保留当前失败目录、`.env` 备份和 Docker 数据卷；不得直接覆盖生产目录，也不得删除未解释的运行数据。先只读确认快照：

```bash
SNAPSHOT=/home/fox/WeKnora/backups/releases/20260820-063053-pre-ui-final
ARCHIVE="$SNAPSHOT/images/core-images.tar.zst"
EXPECTED_ARCHIVE_SHA256=1cf208bf9a5414eec3d8871958664ced0d6f525b555327250b06f08b8112b74b
ROLLBACK_VERSION=rollback-before-ui-final-20260820-063053
test -d "$SNAPSHOT"
find "$SNAPSHOT" -maxdepth 2 -type f -printf '%P\n' | sort
printf '%s  %s\n' "$EXPECTED_ARCHIVE_SHA256" "$ARCHIVE" | sha256sum -c -
zstd -t "$ARCHIVE"
git bundle verify "$(find "$SNAPSHOT" -type f -name '*.bundle' -print -quit)"

APP_ID_BEFORE=$(docker inspect WeKnora-app --format '{{.Image}}')
DOCREADER_ID_BEFORE=$(docker inspect WeKnora-docreader --format '{{.Image}}')
zstd -dc "$ARCHIVE" | docker load

ROLLBACK_FRONTEND=weknora-backup-frontend:${ROLLBACK_VERSION}
ROLLBACK_FRONTEND_ID=$(docker image inspect "$ROLLBACK_FRONTEND" --format '{{.Id}}')
test "$ROLLBACK_FRONTEND_ID" = sha256:3acca6892a6097456715486eb5ed45ce0a69b8fb66cd3797a012e360c98cc902

docker image tag "$APP_ID_BEFORE" "wechatopenai/weknora-app:${ROLLBACK_VERSION}"
docker image tag "$ROLLBACK_FRONTEND" "wechatopenai/weknora-ui:${ROLLBACK_VERSION}"
docker image tag "$DOCREADER_ID_BEFORE" "wechatopenai/weknora-docreader:${ROLLBACK_VERSION}"

cp -a .env ".env.failed-$(date +%Y%m%d-%H%M%S)"
ENV_TMP=$(mktemp ./.env.rollback.XXXXXX)
awk -v version="$ROLLBACK_VERSION" '
  /^WEKNORA_VERSION=/ { print "WEKNORA_VERSION=" version; found = 1; next }
  { print }
  END { if (!found) print "WEKNORA_VERSION=" version }
' .env > "$ENV_TMP"
chmod 600 "$ENV_TMP"
mv "$ENV_TMP" .env

docker compose -p weknora up -d --no-deps frontend
test "$(docker inspect WeKnora-frontend --format '{{.Image}}')" = "$ROLLBACK_FRONTEND_ID"
test "$(docker inspect WeKnora-app --format '{{.Image}}')" = "$APP_ID_BEFORE"
test "$(docker inspect WeKnora-docreader --format '{{.Image}}')" = "$DOCREADER_ID_BEFORE"
for i in $(seq 1 30); do
  FRONTEND_HTTP=$(curl -sS -o /dev/null -w '%{http_code}' https://know.seeway.co/ || true)
  [ "$FRONTEND_HTTP" = 200 ] && break
  sleep 1
done
test "$FRONTEND_HTTP" = 200
```

完成命令后还必须用真实浏览器检查登录、知识库目录切换和一条基础问答；HTTP 200 不能替代页面渲染验收。只有确认数据或迁移损坏时，才改用 `20260819-154754` 全量快照，停止全部写入并恢复 PostgreSQL dump、MinIO 及其他卷；恢复后仍按 `migration -> docreader -> app -> frontend` 的顺序验收。

### 11.2 回滚 migration 000086

只有在确认必须放弃持久化文件夹和可续传上传能力、且已经完成数据库备份后，才允许回滚 `000086`。该 migration 的 down 文件会删除 `knowledge_folders`、`knowledge_upload_sessions` 和 `knowledge_upload_parts` 表，相关空文件夹记录、上传会话和分片进度会丢失；已经落入正式文件存储的知识文件不会因为 down 迁移自动恢复旧业务状态。

使用发布前不可覆盖的 app rollback 标签执行一次 down 迁移。发布前镜像不包含 `000086` 文件，因此只读挂载当前已审核的迁移目录；执行前必须确认数据库恰好位于 `86|f`。变量在本代码块内定义，全新 shell 会话不依赖部署阶段残留变量，也不会退化到 `latest`：

```bash
cd /home/fox/WeKnora
ROLLBACK_TAG=rollback-before-large-upload-20260819-154754
ROLLBACK_APP_IMAGE="wechatopenai/weknora-app:${ROLLBACK_TAG}"
test -f migrations/versioned/000086_knowledge_folders_and_upload_sessions.down.sql
ROLLBACK_APP_IMAGE_ID=$(docker image inspect "$ROLLBACK_APP_IMAGE" --format '{{.Id}}')
test -n "$ROLLBACK_APP_IMAGE_ID"
test "$(docker exec WeKnora-postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT version, dirty FROM schema_migrations;"')" = '86|f'

WEKNORA_VERSION="$ROLLBACK_TAG" \
docker compose -p weknora run --rm --no-deps \
  -v "$PWD/migrations/versioned:/app/migrations/versioned:ro" \
  --entrypoint /bin/sh app \
  -lc 'DB_URL=$(python3 -c "import os, urllib.parse as u; e=os.environ; print(\"postgres://{}:{}@{}:{}/{}?sslmode=disable\".format(u.quote(e[\"DB_USER\"], safe=\"\"), u.quote(e[\"DB_PASSWORD\"], safe=\"\"), e[\"DB_HOST\"], e[\"DB_PORT\"], e[\"DB_NAME\"]))"); exec migrate -path /app/migrations/versioned -database "$DB_URL" down 1'
```

回滚后必须确认 `schema_migrations` 为 `85|f`，并停止使用依赖 `000086` 的 app/frontend 镜像；如果只是回滚业务镜像而保留数据库迁移，应继续使用 `AUTO_MIGRATE=false`，先确认旧版本不会访问新表。

### 11.3 回滚 app 镜像

仅撤回 `80e66d56` 的 MinerU gate 时使用 `20260820-075247-pre-parser-gate` 标签，不降 migration、不切 frontend/docreader：

```bash
cd /home/fox/WeKnora
ROLLBACK_VERSION=rollback-before-parser-gate-20260820-075247
ROLLBACK_APP=weknora-backup-app:${ROLLBACK_VERSION}
ROLLBACK_FRONTEND=weknora-backup-frontend:${ROLLBACK_VERSION}
ROLLBACK_DOCREADER=weknora-backup-docreader:${ROLLBACK_VERSION}
ROLLBACK_APP_ID=$(docker image inspect "$ROLLBACK_APP" --format '{{.Id}}')
test "$ROLLBACK_APP_ID" = sha256:260fc90a1575235c268d7f850009d1f02d93b437ca5f14313b56a7b518e1a111

FRONTEND_ID_BEFORE=$(docker inspect WeKnora-frontend --format '{{.Image}}')
DOCREADER_ID_BEFORE=$(docker inspect WeKnora-docreader --format '{{.Image}}')
docker image tag "$ROLLBACK_APP" "wechatopenai/weknora-app:${ROLLBACK_VERSION}"
docker image tag "$ROLLBACK_FRONTEND" "wechatopenai/weknora-ui:${ROLLBACK_VERSION}"
docker image tag "$ROLLBACK_DOCREADER" "wechatopenai/weknora-docreader:${ROLLBACK_VERSION}"

cp -a .env ".env.failed-$(date +%Y%m%d-%H%M%S)"
ENV_TMP=$(mktemp ./.env.rollback.XXXXXX)
awk -v version="$ROLLBACK_VERSION" '
  /^WEKNORA_VERSION=/ { print "WEKNORA_VERSION=" version; versionDone = 1; next }
  /^AUTO_MIGRATE=/ { print "AUTO_MIGRATE=false"; migrateDone = 1; next }
  { print }
  END {
    if (!versionDone) print "WEKNORA_VERSION=" version
    if (!migrateDone) print "AUTO_MIGRATE=false"
  }
' .env > "$ENV_TMP"
chmod 600 "$ENV_TMP"
mv "$ENV_TMP" .env

docker compose -p weknora up -d --no-deps app
test "$(docker inspect WeKnora-app --format '{{.Image}}')" = "$ROLLBACK_APP_ID"
for i in $(seq 1 90); do
  APP_HEALTH=$(docker inspect WeKnora-app --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}')
  [ "$APP_HEALTH" = healthy ] && break
  [ "$APP_HEALTH" = unhealthy ] && exit 1
  sleep 1
done
test "$APP_HEALTH" = healthy
test "$(docker inspect WeKnora-frontend --format '{{.Image}}')" = "$FRONTEND_ID_BEFORE"
test "$(docker inspect WeKnora-docreader --format '{{.Image}}')" = "$DOCREADER_ID_BEFORE"
for i in $(seq 1 30); do
  PUBLIC_HTTP=$(curl -sS -o /dev/null -w '%{http_code}' https://know.seeway.co/ || true)
  [ "$PUBLIC_HTTP" = 200 ] && break
  sleep 1
done
test "$PUBLIC_HTTP" = 200
```

该 app 回滚镜像正是本次发布前运行的 `a9a09aab` 功能基线，与当前 `86|f` 数据库兼容；回滚固定保留 `AUTO_MIGRATE=false`。如果上述标签已被 Docker 清理，先按 11.1 加载核心镜像归档，再使用其中相同 ID 的 app 备份标签。

### 11.4 回滚公司模型采用

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

### 11.5 回滚源码目录

只有当前 Git 仓库损坏且无法通过正常 Git 操作恢复时，才使用：

```text
/home/fox/WeKnora-backup-20260817-215603
```

目录切换前必须停止将要重建的核心容器，并保留失败目录，不得直接删除。

## 12. 已知非阻塞项

- 前端依赖安装曾报告 2 个 moderate、6 个 high npm audit 告警；本次未做无关依赖升级。
- Vite 仍提示若干产物超过 500 kB；本次不在功能修复中改动拆包策略，后续应以首屏性能数据决定动态加载边界。
- 2026-08-20 新建云端向量验收库的问答受到 SiliconFlow `deepseek-ai/DeepSeek-V4-Flash` 查询理解请求超时影响；日志记录 `context deadline exceeded`，约 60 秒后 SSE 断开，前端未收到终态。向量模型初始化、文档解析和向量检索已通过；在模型服务恢复后必须补做一次该库的问答验收，不能以本次结果替代。
- DocReader 的完整 Python 测试中有 12 项依赖仓库未提供的大型文档夹具而跳过；当前内置夹具、生产 DOCX、图片 OCR 和聊天附件均已通过，但新增这些夹具后仍应在 CI 补跑对应 PDF、PPTX、XLSX 回归。
- RustSec 当前仅报告 `ttf-parser 0.25.1` 的 `RUSTSEC-2026-0192` 停止维护警告，没有安全漏洞；该警告按现有 CI 策略允许通过，后续随 anydoc 上游依赖升级处理。
- MCP wheel 构建仍报告 setuptools 许可证元数据弃用和 `install_requires` 被 `pyproject.toml` 覆盖的警告；当前产物和内容校验通过，但应在 2027-02-18 前统一为 SPDX `license`/`license-files` 并消除 setup.py 重复依赖声明。
- `UploadConfirmMode` 仍保留当前没有调用方的 `'url'` 字面模式；实际网页导入统一使用 `mode: 'file' + urls[]`。未来直接启用 `'url'` 前必须补齐 Dialog 契约。
- 上传确认和过期标签的组件测试仍以源码约束为主；Pinia Promise 已有真实测试，关键 Vue/Transition/Teleport 时序继续由 ego 生产回归覆盖。
- frontend 未定义 Docker healthcheck，必须额外做 HTTP 200 和浏览器渲染验收。
- 构建缓存不能放在仓库根目录 `.cache`，否则会被纳入 app build context，造成数 GB 无效上下文。
- Docker daemon 拉基础镜像不一定继承命令行代理；已有基础镜像应保留，网络异常时先区分 daemon 拉取与容器内下载。
- 不要把 `config/builtin_models.yaml` 替换为含明文凭据的完整模型配置；当前采用清单专门用于复用数据库内已有安全配置。

## 13. 最终交付检查表

- [x] 后端生产功能代码截至 `80e66d56`、前端入口修复截至 `acb62eb6` 已审核、提交并推送。
- [x] 生产功能提交在本地、origin 和远端三方一致。
- [x] 远端 Git 工作树干净且 HEAD 正确。
- [x] `.env`、模型状态和旧镜像已备份。
- [x] app `deploy-80e66d56` 包含 anydoc、迁移、Skill 和共享 MinerU gate，已重建并恢复 healthy。
- [x] migration 已应用到 000086 且状态 clean。
- [x] docreader 保持 healthy 且本次未重启。
- [x] app 保持 healthy；生产 `.env` 已备份并收敛为 `AUTO_MIGRATE=false`。
- [x] frontend 在 app healthy 状态下切换并返回 HTTP 200。
- [x] 上传队列入口仅在知识库文档详情页显示，聊天页实机验证为 0 个入口。
- [x] 平台解析配置存在，知识文件夹和上传会话表存在。
- [x] 可续传上传配置为 2 GiB、4 MiB、24 小时；前端默认单活动上传且没有人为带宽限速。
- [x] 公司预置模型数量正确且普通用户脱敏。
- [x] Skill ZIP、Chrome CRX 和 Chrome ZIP 可下载。
- [x] 知识库和临时附件端到端解析通过。
- [x] 持久化空文件夹创建、目录树展示和空文件夹删除通过。
- [x] 可续传上传初始化、分片校验、进度查询、取消和清理通过。
- [x] 2026-08-20 本轮新增验收文档、文件夹、上传会话和聊天会话已清理。
- [ ] 新建 `qwen3向量化模型` 云端向量库的问答需在 SiliconFlow 恢复后补验；本轮仅完成解析、嵌入初始化和向量检索验证。
- [x] DocReader Python/Go、MCP 四版本、anydoc Rust/Go 和 CLI race 门槛已补跑并记录。
- [x] MCP `tests/` 下 pytest 用例已纳入 CI，独立子代理复审无 P0/P1。
- [x] 切换后无新的容器崩溃；当前唯一 ERROR 为 10.7 已记录的 SiliconFlow 查询理解超时，不属于 frontend 部署错误。
- [x] 回滚路径和备份文件可读。
