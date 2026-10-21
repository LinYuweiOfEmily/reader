# 论文来源可靠性与网页导入

用户已确认前文方案，目标是在现有 Go 客户端中减少重复请求和限流，并改善网页元数据识别。仅 Windows 验证，不改模型和代理选择，不读取登录凭据。采用 superpowers:subagent-driven-development 的独立实现与审查流程。

## 设计
- 所有 scholar 请求（含重定向）经过共享来源调度；arXiv API/PDF 共用串行队列和至少 3 秒间隔，S2 1.1 秒，其他公开站点保守串行。取消等待须即时生效。
- 429/503 的 Retry-After 转为来源冷却，不让重复点击再次访问。无 Retry-After 的 429 默认冷却 60 秒。过长冷却直接告知剩余时间，不占用交互线程。
- GET 临时网络/502/503/504 故障最多重试两次，总预算受调用 context 限制；不重试权限/证书/404。保留主机和错误类别，不输出密钥或查询文本。
- 仅成功的结构化元数据缓存，至多 128 项/16 MiB，每项 15 分钟；不缓存 PDF、网页、错误或凭据。相同请求合并，等待者取消不打断其他请求，首个请求取消后其他调用可重新发起。
- 推荐单来源预算改为 35 秒；依旧先 arXiv 后 S2，不在限流时循环撞站点。版本 PDF 仅 404/410 才回退最新版。
- 网页参考 Zotero 的嵌入元数据分层思路，独立实现，不引入其运行时或复制代码。优先 citation 标签，补充 Dublin Core 和 ScholarlyArticle JSON-LD，按优先级去重并最多尝试 3 个 PDF 候选。保持大小/URL 检查，不执行网页脚本。
- 当前已经具备缓存和串行处理的 easyScholar 保留现状；本次集中解决论文来源，不添加未提供的 S2 API Key，也不扩大为代理设置改版。

## 步骤与验证
- [ ] 写网络回归测试并观察失败：合并、冷却、重试、取消、缓存边界、串行限速、重定向。
- [ ] 实现 scholar 网络层，运行 scholar 测试；提交独立网络修复。
- [ ] 写并实现网页结构化元数据/候选 PDF 测试；独立提交导入改进。
- [ ] 审查取消、资源释放、缓存上限和错误分类，完成 pnpm typecheck/test/build。全量既有 Windows 测试失败须如实记录。
- [ ] 更新使用文档；只提交本任务文件，不推送、不覆盖安装中应用。用户本轮未要求重新打包，交付源码和验证结果。

## 参考
- https://info.arxiv.org/help/api/tou.html
- https://github.com/hashicorp/go-retryablehttp/blob/main/client.go
- https://pkg.go.dev/golang.org/x/sync/singleflight
- https://www.zotero.org/support/dev/translators
- https://schema.org/ScholarlyArticle
