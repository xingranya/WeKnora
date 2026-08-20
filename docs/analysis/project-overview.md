# WeKnora 生产发布修复项目概览

## 目标

以当前本地工作树为唯一候选代码源，收口认证迁移、聊天流、审计日志、DocReader、上传与发布门禁，完成全量验证、独立审查、生产备份、兼容发布和 ego 真实验收。

## 技术栈与入口

| 层 | 技术与入口 | 主要验证 |
|---|---|---|
| Web 前端 | Vue 3、TypeScript、Pinia、Vite，入口 `frontend/src` | Vitest、type-check、Vite build、Playwright/ego |
| API 服务 | Go、Gin、GORM，入口 `cmd/server/main.go` | `go test ./...`、race、健康检查 |
| 数据库 | PostgreSQL，版本迁移位于 `migrations/versioned` | 真实 PostgreSQL 86 -> 87 -> 86 |
| 文档解析 | Python gRPC DocReader、Go parser adapter、MinerU 3.4.5 | unittest、Go parser tests、真实文档 |
| 文件存储 | local/MinIO 等 provider、可续传分片会话 | SHA256、断点恢复、配额和隔离测试 |
| 部署 | Docker Compose、Nginx、宿主机 MinerU systemd | compose config、镜像构建、分阶段切换 |

## 当前生产基线

核对时间：2026-08-20，远端 `/home/fox/WeKnora`。

- Git：`cc209f2537b57967b96ff9e0a090fcc0c362214c`，分支 `codex/jiwai-branding`，工作树干净。
- app：镜像 `sha256:ee81fc32b8d24e522bacd52eb9e87976f26c6c218a006909e1ccecec68c670db`，healthy，restart 0。
- frontend：镜像 `sha256:70556b283a3f37f28bbbd9d8d916d39d8319b0b8965c14c4664d6e57708b786d`，running，restart 0。
- docreader：镜像 `sha256:b9c4636b65b5d4947d5e09cd311ba6cf37f1f2da37c51d4be2b911d432f12abe`，healthy，restart 0。
- PostgreSQL：migration `86|false`；`weknora_*` Docker 卷 10 个。
- MinerU：3.4.5 systemd active，`NRestarts=0`，并发 2。
- 本机及服务器无残留 docker build、rsync、预检容器或测试服务。

## 跟踪与治理

- 跟踪模式：`LOCAL_ONLY`。fork `xingranya/WeKnora` 的 GitHub Issues 未启用，不向上游仓库写入 Issue。
- 共享指令：会话注入的根级 `AGENTS.md` 约束；`cli/AGENTS.md` 仅适用于 CLI 子树。
- 持久记忆：使用 Codex 原生项目记忆作为只读上下文；本轮未获用户明确授权，不写入或新建仓库记忆文件。
- 权威发布定义：[`docs/plan/生产发布修复Spec.md`](../plan/生产发布修复Spec.md)。
