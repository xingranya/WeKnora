# WeKnora 生产发布修复进度

模式：`LOCAL_ONLY`

权威 Spec：[`../plan/生产发布修复Spec.md`](../plan/生产发布修复Spec.md)

分析：[`../analysis/project-overview.md`](../analysis/project-overview.md)、[`../analysis/module-inventory.md`](../analysis/module-inventory.md)、[`../analysis/risk-assessment.md`](../analysis/risk-assessment.md)

## Phase 状态

- [x] [Phase 1：现场与 Spec](phase-1-spec.md) (1/1)
- [x] [Phase 2：P0/P1 修复](phase-2-fixes.md) (5/5)
- [ ] [Phase 3：全量门禁](phase-3-gates.md) (0/5)
- [ ] [Phase 4：生产发布](phase-4-release.md) (0/3)
- [ ] [Phase 5：真实验收](phase-5-acceptance.md) (0/2)

## 当前状态

- 当前 Phase：3，执行全量测试、真实 PostgreSQL、镜像构建和最终独立复审；生产保持 `cc209f25` 未变。
- 当前发布结论：`NO-GO`，P0/P1 尚未清零。
- 已关闭残留：DocReader 预检构建已失败退出；本机/服务器无未知后台任务。
- 外部阻塞：无。代理曾返回 Debian 包 502，Dockerfile 已增加有限 APT 重试，待最终重建验证。

## 下一步

1. 执行 Go 全包、前端、DocReader、真实 PostgreSQL 和最终镜像门禁。
2. 使用 Playwright 做候选隔离视口验证。
3. 最终四组只读复审，P0/P1 清零后才进入备份和发布。

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
