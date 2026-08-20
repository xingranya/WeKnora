# 生产风险评估

## 发布阻断项

| 风险 | 裁定 | 影响 | 门禁 |
|---|---|---|---|
| `000087` 原位覆盖 token | 已复现/代码确定 P0 | 旧镜像无法识别已哈希 token，回滚会使用户掉登录 | 双列迁移和真实 PostgreSQL 旧新兼容测试 |
| SSE 在 `answer.done` 结束 | 代码确定 P1 | 丢 references、complete、title，页面长期 loading | 真实事件顺序测试和浏览器验收 |
| Quick Answer panic 无终态 | 代码确定 P1 | SSE 等待不结束，失败消息未落库 | panic 定向测试与持久化断言 |
| 日志凭据旁路 | 代码确定 P1 | request-id 路径、Python/Base64、opaque token 可能泄漏 | 多日志脱敏测试和权限检查 |
| 预签名租户数字段猜测 | 代码确定 P1 | 数字 bucket/prefix 可能跨租户取错授权上下文 | provider 布局和跨租户测试 |
| DocReader 取消不深入解析循环 | 代码确定 P1 | 大 PDF 取消后仍占用 CPU/RSS | 锁等待、页循环和子进程取消测试 |
| gRPC `conn != nil` 视为健康 | 代码确定 P1 | 失败重连覆盖旧健康连接 | health/ListEngines 后原子替换 |
| HTTP DocReader 限制弱于 gRPC | 条件性风险 P1 | 标准生产若误配 HTTP 可绕过大小/健康契约 | 标准 release 启动拒绝 HTTP |
| DocReader 镜像构建代理 502 | 已复现，非代码缺陷 | 单个 Debian 包下载失败 | APT 有限重试，最终镜像重建 |

## 当前生产不适用或延后项

- 候选代码污染生产：现场核对为误报，远端仍是干净 `cc209f25`。
- MinerU 2.7 残留：当前生产不适用，宿主机仅 3.4.5 服务 active。
- 生产 HTTP DocReader：当前配置为 gRPC；仍增加 release 门禁防止未来误配。
- 移除 `auth_tokens.token` 明文列：本轮明确禁止，等待回滚窗口结束后独立 contract migration。
- 上传限速：用户已取消限速要求，本轮不得重新引入 9Mbps token bucket。

## S.U.P.E.R 健康摘要

- S：解析器和 SSE 编排已有合理模块边界，但 handler 和 PDF parser 仍是复杂热点。
- U：主要依赖方向正确；`cmd/server` 对 handler edition 常量的反向依赖需避免扩大。
- P：gRPC/SSE/schema 有显式契约，但 storage path 和审计日志仍需确定规则。
- E：大部分配置环境化；迁移脚本 source `.env`、浮动工具版本和硬编码端口违背环境无关原则。
- R：provider/parser 可替换性较好；认证 schema 和旧镜像兼容必须通过 expand migration 保持。

结论：P0/P1 未清零前为 `NO-GO`。
