# WeKnora 生产发布修复 Spec

状态：执行中

日期：2026-08-20

候选来源：当前本地工作树

生产基线：`cc209f2537b57967b96ff9e0a090fcc0c362214c`

## 1. 问题清单与证据

| ID | 问题 | 证据/复现 | 根因 | 状态 |
|---|---|---|---|---|
| AUTH-01 | token 原位哈希不可回滚 | 旧 app 只按 `token` 明文查找；原 000087 覆盖该列 | 没有 expand/contract 窗口 | 修复中 P0 |
| CHAT-01 | `answer.done` 丢引用和标题 | 代码在正文 done 时 abort/finish | 把正文终态混同整条 SSE 终态 | 修复中 P1 |
| CHAT-02 | hydration、panic、stop、历史和移动端缺陷 | 四组独立审查的代码路径与定向用例 | Promise/终态/响应式边界不完整 | 修复中 P1 |
| AUDIT-01 | 多日志路径可泄漏凭据 | Go、LLM JSONL、Python 和 request-id 文件名规则不一致 | 缺少统一语义和结构化边界 | 修复中 P1 |
| FILE-01 | storage path 首个数字段猜租户 | 数字 bucket/region/prefix 会被误判 | 路径契约未按 provider 定义 | 修复中 P1 |
| PARSER-01 | 取消、图片预算和连接健康不完整 | 锁等待/页循环不读取消；`conn != nil` 被视为健康 | 限制只停留在外层 | 修复中 P1 |
| UPLOAD-01 | 默认暂存根、原子身份和 finalize 风险 | 默认路径可跳过校验，崩溃可能留下半写 identity | 共享存储契约不完整 | 修复中 P1 |
| DEPLOY-01 | 自动迁移、脚本 TLS 和构建漂移 | Compose 默认自动恢复；脚本 source `.env`；migrate 使用 latest | 发布门禁 fail open | 修复中 P0/P1 |
| BUILD-01 | DocReader 镜像代理下载 502 | 运行阶段 apt 获取单包返回代理 502，exit 100 | 外部依赖下载抖动 | 代码已加有限重试，待重建 |

## 2. 修改范围

- 认证：`auth_tokens.token_fingerprint` expand migration、双读双写、refresh/revoke/logout 兼容。
- 聊天：后端稳定终态与失败持久化；前端完整消费 SSE；hydration 去重；移动抽屉与历史重试。
- 审计：保留问题、正文片段、摘要、标题、文件名、URL、模型/解析结果和第三方错误详情；只过滤认证秘密。
- 文件：provider 确定布局的租户解析、签名 TTL/缓存、SSRF 白名单拆分。
- 解析：gRPC 健康替换、配置单位、Anydoc 回退上限、PDF 预算和取消传播；标准生产禁用 HTTP transport。
- 上传：2GiB/4MiB 会话契约、流式 SHA256、原子 identity、共享 RWX 校验和 finalize 并发。
- 发布：固定构建版本、显式迁移、持久化审计卷、健康检查、完整备份和分阶段切换。

不在范围：新增产品功能、重新引入上传限速、删除旧 token 明文列、升级无关依赖。

## 3. 兼容约束

1. migration 87 后旧 app、新 app 和短时混合部署都能认证；down 只删除 fingerprint。
2. `answer.done` 仅结束正文，整流继续到服务端 EOF；客户端不得主动 abort 正常终态。
3. 客户端和消息表只保存稳定错误码/文案；脱敏后的完整业务错误留在审计日志。
4. 公司业务审计内容可查，密码、API Key、JWT、Cookie、Authorization、私钥和签名参数不可恢复。
5. 生产仍使用 MinerU 3.4.5 和 gRPC DocReader；并发配置不在本轮扩大。
6. 上传不设置人为带宽上限；默认一个活动文件，服务端仍执行会话/容量/finalize 限制。

## 4. 测试矩阵

| 层 | 必须通过 |
|---|---|
| 静态 | `git diff --check`、shell syntax、Python compile |
| Go | `go test ./...`；docparser、上传、session/stream 执行 race |
| 前端 | 全量 Vitest、type-check、production build、Vue 真实挂载 |
| DocReader | 全量 unittest；取消、图片预算、配置单位、健康服务 |
| PostgreSQL | 86 -> 87、重复、旧新查找/双写、refresh/revoke/logout、87 -> 86、dirty fail-close |
| Docker | compose config；app/frontend/docreader 最终镜像构建和健康 |
| 文件 | 小文件、50MiB、DOCX、文本/扫描 PDF、2GiB 边界与断点恢复 |
| 浏览器 | 桌面与 390x844、375x812 的聊天、目录、队列、引用、标题和权限 |

## 5. 发布顺序

1. P0/P1 清零并完成四组独立只读复审。
2. 精确暂存并形成可复现原子提交，推送 origin。
3. 保存代码、`.env`、Compose、数据库、核心/运行镜像、全部卷、MinerU 和 SHA256 清单。
4. 将远端源码快进到候选提交，显式执行 migration `up 1`。
5. 切换 docreader，验证健康与基线解析。
6. 切换 app，验证健康、schema、旧会话和 API。
7. 切换 frontend，验证 HTTP 和浏览器渲染。
8. ego 桌面/移动真实验收，监控重启、RSS、队列和错误日志。

## 6. 回滚方案

- 常规回滚：恢复旧 `.env`/Compose，加载不可覆盖核心镜像标签，先 frontend/app/docreader 分层回退；migration 87 的新增列可被旧 app 忽略。
- schema 回滚：在无候选写入时显式执行 `down 1`，只删除 `token_fingerprint` 索引和列；旧 `token` 始终保留。
- 灾难回滚：停止写入，恢复 PostgreSQL dump 和卷归档，再加载全部运行镜像归档。
- 任一迁移失败、核心容器不健康、认证回归、凭据泄漏、SSE 无终态、解析基线失败或回滚资产校验失败，立即判定 `NO-GO`。

## 7. 验收结果

当前为执行中。已确认生产未被候选代码污染，基础现场见 [`project-overview.md`](../analysis/project-overview.md)。最终结果必须补录 commit、镜像 ID、schema、备份目录、测试数据、ego 截图/事件证据和清理结果。
