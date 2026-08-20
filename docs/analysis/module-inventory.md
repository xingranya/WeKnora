# 模块清单与 S.U.P.E.R 评估

评分：5 为健康，1 为高风险。S=单一职责，U=单向依赖，P=接口契约，E=环境无关，R=可替换性。

| 模块 | 职责 | S | U | P | E | R | 本轮结论 |
|---|---|---:|---:|---:|---:|---:|---|
| `internal/models/chat` | 模型供应商适配与流式响应 | 4 | 4 | 4 | 4 | 4 | SiliconFlow V4 字段已按官方契约适配，需守住公开错误边界 |
| `internal/handler/session` | SSE 编排、会话终态和持久化 | 3 | 3 | 3 | 4 | 3 | answer/complete/stop 语义存在跨端耦合，属 P1 |
| `frontend/src/api/chat` | 浏览器 SSE 消费 | 4 | 4 | 3 | 4 | 4 | 不能把 `answer.done` 当全流终态 |
| `frontend/src/views/chat` | 会话 hydration、历史与移动布局 | 3 | 3 | 3 | 4 | 3 | Promise 去重和移动抽屉需真实挂载验证 |
| `internal/application/repository` | 认证 token、业务持久化 | 4 | 4 | 4 | 4 | 4 | token 迁移必须 expand/contract 分阶段 |
| `internal/logger` / middleware | 审计输出与脱敏 | 4 | 4 | 3 | 4 | 4 | 多日志实现必须统一语义，不得删除业务正文 |
| `internal/router/files` / storage URL | 文件代理与租户授权 | 4 | 4 | 3 | 4 | 3 | 租户 ID 不能靠首个数字段猜测 |
| `internal/infrastructure/docparser` | parser 选择、gRPC/HTTP 适配 | 3 | 4 | 4 | 4 | 4 | 连接健康、大小契约和传输能力需 fail closed |
| `docreader` | 文档解析、PDF 图片输出 | 3 | 4 | 4 | 4 | 4 | 取消和资源预算需深入页循环与锁等待 |
| `knowledge_upload` | 分片、恢复、完成与正式发布 | 3 | 4 | 4 | 3 | 3 | 共享暂存根和最终身份发布属于 P1 |
| migrations/scripts | schema 演进与运维入口 | 4 | 4 | 4 | 3 | 4 | `000087` 原位哈希会破坏旧镜像回滚，属 P0 |
| Docker/Compose/Nginx | 构建、健康与运行配置 | 4 | 4 | 3 | 3 | 4 | 固定工具版本、显式迁移、独立日志卷和健康检查 |

## 结构热点

1. 聊天终态同时跨 Go handler、SSE wire event 和 Vue consumer，任何一端提前结束都会丢引用或标题。
2. 认证迁移必须同时满足旧 app、新 app 和混合部署，数据库列是兼容契约而非单纯存储细节。
3. DocReader 的资源限制横跨配置单位、gRPC 消息、PDF 原始图、Base64 和客户端适配，必须使用同一字节契约。
4. 审计日志有 Go 主日志、LLM JSONL、Python 日志三条路径，必须共享“保留业务、过滤凭据”的语义。
