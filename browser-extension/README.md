# 见外知识库浏览器插件

本目录是见外知识库助手 `v1.3.2` 的可维护源码。初始文件来自已发布的 Manifest V3 安装包，后续品牌、登录、动态网页采集和打包修改均以本目录为准。

## 当前约定

- 知识库 API 固定为 `https://know.seeway.co/api/v1`，用户只填写 API Key。
- 普通请求使用 12 秒超时，popup 到后台消息使用 15 秒超时；验证使用轻量 `/auth/me` 接口，并区分凭证、权限、网络、超时和扩展后台故障。
- API Key 仅保存在 `chrome.storage.local`，请求使用 `X-API-Key`。
- `collection.js` 负责识别侧栏、折叠目录、组件树路由和虚拟滚动中的文档链接。
- `extractors.js` 负责动态网页、Shadow DOM、同源 iframe 和虚拟滚动采集。
- 文档集任务使用独立非活动标签页逐页处理，支持递归发现、URL 去重、暂停、继续、取消、失败重试和浏览器重启恢复，默认最多采集 50 篇。
- 手动框选支持移动、八向缩放和自动滚动长截图；截图以内嵌图片随 Markdown 一起写入知识库。
- 飞书富文档按块语义保留标题、表格、列表、Callout、链接和图片；blob 图片会转为 data URI，由 WeKnora 后端落盘。
- 巨量帮助中心会等待内嵌飞书 frame 就绪，外层短壳不再作为正文；图片优先使用真实 `data-src`，加载失败 SVG 会转为附件或不可加载说明。
- 站点适配覆盖飞书、巨量引擎、抖音规则与生活服务学习中心、腾讯文档、语雀、Notion、WPS、钉钉文档、Google 文档和 Microsoft 365 文档。
- `defuddle.js` 继续负责普通文章页的正文识别和 Markdown 转换。

## 验证

```bash
bash scripts/verify.sh
```

脚本会检查 Manifest、JavaScript 语法，运行后台任务状态机测试，并使用本机 Chrome 运行动态 DOM 夹具测试。

## 打包

发布时从本目录生成 ZIP，并使用单独保管的私钥生成 CRX3。私钥不得进入 Git、安装包、日志或部署目录。
