# WeKnora 生产发布修复进度

模式：`PRODUCTION_DEPLOYED`

权威 Spec：[`../plan/生产发布修复Spec.md`](../plan/生产发布修复Spec.md)

分析：[`../analysis/project-overview.md`](../analysis/project-overview.md)、[`../analysis/module-inventory.md`](../analysis/module-inventory.md)、[`../analysis/risk-assessment.md`](../analysis/risk-assessment.md)

## Phase 状态

- [x] [Phase 1：现场与 Spec](phase-1-spec.md) (1/1)
- [x] [Phase 2：P0/P1 修复](phase-2-fixes.md) (5/5)
- [x] [Phase 3：全量门禁](phase-3-gates.md) (5/5)
- [x] [Phase 4：生产发布](phase-4-release.md) (3/3)
- [x] [Phase 5：真实验收](phase-5-acceptance.md) (2/2)

## 当前状态

- 当前 Phase：5 已完成，版本已部署并完成真实验收。
- 当前发布结论：`GO`。
- 部署源码：`3df8ccbfe1ca7fd423e2d8c0ddcd3f1c7e347544`。
- 生产：三核心容器 healthy、restart 0，schema `87 clean`，MinerU 3.4.5 active。
- 回滚点：`/home/fox/WeKnora/backups/releases/20260819-154754`，约 13 GiB，
  241 个文件 SHA256 通过，失败 0。
- 备份例外：依发布指令复用前一日备份。普通镜像回滚可用，灾难恢复 RPO
  为 2026-08-19 15:47:54 左右。
- 完整交接：
  [`../releases/2026-08-20-production-release.md`](../releases/2026-08-20-production-release.md)。

## 下一步

1. 观察生产错误率、容器重启、上传会话和 MinerU 队列。
2. 下一次生产变更前重新制作当日完整备份，收敛灾难恢复 RPO。
3. 发布后处理标题 3 秒刷新、2 GiB 实测、MinerU 长稳压测和扩展逐站回归。

## 治理状态

- 指令：根级会话 `AGENTS.md` 为共享规则；`cli/AGENTS.md` 仅覆盖 CLI 子树。
- 记忆：Codex 原生项目记忆仅作只读上下文；未创建竞争性的仓库记忆文件。
- GitHub：fork Issues 未启用，因此采用 LOCAL_ONLY，不写上游 Issue/PR 状态。

## Adaptive Control State

```yaml
phase_1: {tasks: 1, drift_score: 0, annotate: 1, replan: 1, rescope: 1}
phase_2: {tasks: 5, drift_score: 0, annotate: 1, replan: 2, rescope: 3}
phase_3: {tasks: 5, drift_score: 0, annotate: 1, replan: 2, rescope: 3}
phase_4: {tasks: 3, drift_score: 0, annotate: 1, replan: 2, rescope: 2}
phase_5: {tasks: 2, drift_score: 0, annotate: 1, replan: 1, rescope: 2}
```

## Task Telemetry Log

| 时间 | 任务 | 实际工作量 | S.U.P.E.R | 未计划依赖 | 结果 |
|---|---|---|---|---:|---|
| 2026-08-20 | T1.1 现场核对与发布 Spec | M | 10/10 | 0 | 完成；生产未被候选污染 |
| 2026-08-20 | T2.1 认证 expand migration 与门禁 | L | 10/10 | 0 | 完成；补充低熵密钥门禁和真实 PostgreSQL 脚本 |
| 2026-08-20 | T2.2 SSE/会话/移动端 | L | 10/10 | 0 | 完成；消除重复移动侧栏按钮并补真实 Vue mount |
| 2026-08-20 | T2.3 审计与文件代理安全 | L | 10/10 | 0 | 完成；签名文件缓存改为 private/no-store |
| 2026-08-20 | T2.4 DocReader/parser/upload | XL | 10/10 | 0 | 完成；最终身份改 SHA-256 并兼容旧 MD5 |
| 2026-08-20 | T2.5 Compose/镜像/脚本 | M | 10/10 | 0 | 完成；显式迁移、固定版本和健康检查已收口 |
| 2026-08-20 | T3.1-T3.5 全量门禁与最终复审 | XL | 10/10 | 0 | 完成；全量、race、真实 PostgreSQL、三镜像和最终复审通过 |
| 2026-08-20 | T4.1-T4.3 原子发布 | L | 10/10 | 1 | 完成；用户接受复用前一日完整备份的 RPO 例外 |
| 2026-08-20 | T5.1-T5.2 ego 验收与交接 | XL | 10/10 | 0 | 完成；桌面、双移动视口、扩展、256 MiB 续传和审计通过 |
