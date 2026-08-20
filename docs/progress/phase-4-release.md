# Phase 4：生产发布

- [x] T4.1 精确暂存、原子提交、推送与三方一致
- [x] T4.2 使用已验证的 13 GiB 完整生产备份和 SHA256/可恢复性校验
- [x] T4.3 显式 `up 1`，按 docreader -> app -> frontend 分阶段发布

## Notes

- 部署源码提交：`3df8ccbfe1ca7fd423e2d8c0ddcd3f1c7e347544`。
- schema：`86 clean -> 87 clean`。
- 最终三个核心容器均 healthy，restart 0。
- 按发布指令取消本轮新备份，复用
  `/home/fox/WeKnora/backups/releases/20260819-154754`；241 个文件 SHA256
  校验通过，失败 0。该用户接受的例外只影响灾难恢复 RPO，不影响普通镜像回滚。
- 详细记录见
  [`../releases/2026-08-20-production-release.md`](../releases/2026-08-20-production-release.md)。
