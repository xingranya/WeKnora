# Phase 3：全量门禁

- [x] T3.1 Go 全量与高风险 race
- [x] T3.2 前端全量、type-check、build 和真实 Vue 挂载
- [x] T3.3 DocReader 全量与最终镜像
- [x] T3.4 真实 PostgreSQL 兼容/回滚/dirty 矩阵
- [x] T3.5 一次最终综合只读复审，P0/P1 清零

## Notes

- Go 全量和高风险 race 通过。
- Frontend 全量 `454/454`、真实挂载 `3/3`、type-check 和 build 通过。
- DocReader `152 passed`、`12 skipped`，最终镜像 gRPC health 为 SERVING。
- 真实 PostgreSQL 86 到 87、重复迁移、旧新查询、并发 refresh、revoke/logout、
  down 和 dirty fail-close 通过。
- 最终综合审查无仍适用于当前生产的 P0/P1。
