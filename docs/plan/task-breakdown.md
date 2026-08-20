# 任务分解

| 任务 | 优先级 | 工作量 | 依赖 | Batch | 验收 |
|---|---|---|---|---|---|
| T1.1 现场核对与发布 Spec | P0 | M | 无 | B1 | 基线、风险、范围、门禁和回滚均落盘 |
| T2.1 认证 expand migration 与门禁 | P0 | L | T1.1 | B2 | 旧新 app、up/down、dirty 和 JWT/AES 门禁通过 |
| T2.2 SSE/会话/移动端收口 | P1 | L | T1.1 | B2 | 完整事件顺序、panic、hydration、历史和双移动尺寸通过 |
| T2.3 审计与文件代理安全 | P1 | L | T1.1 | B2 | 业务内容保留、凭据不可恢复、跨租户测试通过 |
| T2.4 DocReader/parser/upload 收口 | P1 | XL | T1.1 | B2 | 取消、预算、健康、2GiB/恢复和共享存储契约通过 |
| T2.5 Compose/镜像/脚本可复现 | P1 | M | T2.1,T2.4 | B2 | 显式迁移、固定版本、健康检查、compose config 通过 |
| T3.1 全量 Go 与 race | P0 | L | T2.* | B3 | 全包及高风险 race 无失败 |
| T3.2 前端全量与真实挂载 | P0 | M | T2.2 | B3 | Vitest、type-check、build、挂载测试通过 |
| T3.3 DocReader 全量与镜像 | P0 | L | T2.4 | B3 | Python 全量和最终镜像构建通过 |
| T3.4 PostgreSQL 兼容矩阵 | P0 | L | T2.1 | B3 | 86/87、旧新、down、dirty 全通过 |
| T3.5 四组最终只读复审 | P0 | M | T3.1-T3.4 | B3 | P0/P1 清零 |
| T4.1 原子提交与三方同步 | P0 | M | T3.* | B4 | 本地/origin/远端提交一致 |
| T4.2 完整备份与校验 | P0 | XL | T4.1 | B4 | 镜像、卷、DB、配置和 SHA256 可恢复 |
| T4.3 显式迁移与分阶段发布 | P0 | L | T4.2 | B4 | schema 和三核心服务逐步健康 |
| T5.1 ego 桌面/移动真实验收 | P0 | XL | T4.3 | B5 | 用户流程、SSE、上传和解析闭环通过 |
| T5.2 监控、清理与交接 | P0 | M | T5.1 | B5 | 无残留任务/数据，手册和证据完整 |

## 并行执行通道

- Lane A：认证/迁移，仅修改认证 schema、repository、迁移与门禁。
- Lane B：聊天/前端，仅修改 SSE、会话、历史和移动布局。
- Lane C：安全/审计，仅修改日志、公开错误边界、文件代理和脱敏。
- Lane D：解析/上传，仅修改 DocReader、parser adapter 和上传存储契约。

四条 Lane 文件边界原则上分离；`internal/handler/session` 的公开错误边界由 Lane B/C 合并时人工审查。
