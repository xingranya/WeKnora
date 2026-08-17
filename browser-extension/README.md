# 见外知识库浏览器插件

本目录是见外知识库助手 `v1.2.0` 的可维护源码。初始文件来自已发布的 Manifest V3 安装包，后续品牌、登录、动态网页采集和打包修改均以本目录为准。

## 当前约定

- 知识库 API 固定为 `http://100.78.64.62:8080/api/v1`，用户只填写 API Key。
- API Key 仅保存在 `chrome.storage.local`，请求使用 `X-API-Key`。
- `extractors.js` 负责动态网页、Shadow DOM、同源 iframe 和虚拟滚动采集。
- 飞书富文档按块语义保留标题、表格、列表、Callout、链接和图片；blob 图片会转为 data URI，由 WeKnora 后端落盘。
- 站点适配覆盖飞书、抖音规则中心、腾讯文档、语雀、Notion、WPS、钉钉文档、Google 文档和 Microsoft 365 文档。
- `defuddle.js` 继续负责普通文章页的正文识别和 Markdown 转换。

## 验证

```bash
bash scripts/verify.sh
```

脚本会检查 Manifest、JavaScript 语法，并使用本机 Chrome 运行动态 DOM 夹具测试。

## 打包

发布时从本目录生成 ZIP，并使用单独保管的私钥生成 CRX3。私钥不得进入 Git、安装包、日志或部署目录。
