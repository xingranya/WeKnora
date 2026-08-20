# Phase 2：P0/P1 修复

- [x] T2.1 认证 expand migration 与生产门禁
- [x] T2.2 SSE、会话、历史和移动端
- [x] T2.3 审计日志、公开错误与文件代理
- [x] T2.4 DocReader、parser 和上传存储契约
- [x] T2.5 Compose、镜像与迁移脚本收口

## Notes

- 四组子代理按互斥写入范围工作；`internal/handler/session` 交叉点由主代理合并审查。
- 公司业务正文必须保留，认证秘密必须不可恢复。
- 新上传记录使用流式 SHA-256；同次读取生成旧 MD5 兼容键，只用于命中历史记录。
- Presigned 有效期内使用 private cache，过期/非法签名使用 private no-store。
- 定向 Go、race、前端 452 项、真实 Vue 挂载和 DocReader 160 项均通过；全仓门禁在 Phase 3 重跑。
