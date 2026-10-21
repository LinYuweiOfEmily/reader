# 论文来源请求与网页导入

论文推荐、链接导入和元数据补全共用服务端 scholar 客户端。所有请求沿用 Reader 启动时确定的代理，不因失败自动改成直连。

## 请求行为

- arXiv API 和 PDF 共用串行队列，请求开始至少间隔 3 秒。Semantic Scholar 至少间隔 1.1 秒，其余站点至少 250 毫秒。同来源响应体关闭后才释放请求槽，重定向也经过调度。
- 429 默认使来源冷却 60 秒；429/503 提供有效 `Retry-After` 时按其秒数或 HTTP 日期冷却。冷却期间返回剩余等待时间，不反复访问该来源。冷却在进程内共享，重启后不保留。
- 暂时网络错误和 502/503/504 最多重试两次，等待逐步延长并带少量随机间隔。整体仍受原任务预算限制；取消、证书校验错误、403、404 不自动重试。有冷却指令时优先返回等待提示，不在界面内长时间等待。
- 推荐的每个来源预算为 35 秒，整个推荐预算仍为 4 分钟。来源失败后可尝试原有备用来源；不能保证匿名 Semantic Scholar 不限流，也不能解决代理或对方服务故障。
- arXiv 指定版本 PDF 只在 404/410 时回退最新版；网络错误和限流不会触发版本切换。

## 缓存与重复查询

仅缓存成功且语法有效的 JSON/Atom 元数据，15 分钟过期，上限 128 条、响应体合计 16 MiB；单项最多 4 MiB。PDF 和出版社网页不进入该缓存。缓存留在进程内，不新增 C 盘文件，也不提供过期结果。关闭 Reader 后释放。

同一 URL、Accept 和大小上限的并发查询共享一次请求。等待者取消不影响请求所有者；所有者取消后仍需结果的等待者可重新请求。底层重试次数耗尽时共享失败，不因每个等待者再次重试而放大流量。

## 网页导入

沿用专用 arXiv、DOI、OpenReview 等入口。普通网页优先采用 citation 元数据，再补充 Dublin Core 和唯一、明确的 ScholarlyArticle JSON-LD。JSON-LD 只作为数据读取，不运行脚本，不任意遍历参考文献或搜索结果列表。

PDF 优先使用网页声明的 citation、alternate PDF 和 JSON-LD PDF encoding。没有这些声明时才尝试普通 PDF 链接，解析相对地址、去重，并最多尝试三个候选。保留现有公网页址与文件大小检查、取消操作以及标识符回退。

这借鉴了 Zotero 的“网站适配与嵌入元数据”思路，代码为独立实现；未集成 Zotero 运行时或其全部网站转换器。需要登录、动态脚本或人工验证的页面仍可能需要浏览器下载后导入。

## 诊断

服务端日志中的 `scholar.request` 记录来源主机、尝试序号、耗时、状态及错误类别，不记录完整 URL、检索词、响应体或认证信息。缓存命中不产生外部请求日志。

设置中的旧连接检测仍是通用连通性检测，不能证明每个论文来源都可用。Semantic Scholar 专用 API Key 的配置界面不在本次范围内。

## 参考

- [arXiv API 规则](https://info.arxiv.org/help/api/tou.html)
- [go-retryablehttp 的重试策略](https://github.com/hashicorp/go-retryablehttp/blob/main/client.go)
- [Zotero Translators](https://www.zotero.org/support/dev/translators)
- [Schema.org ScholarlyArticle](https://schema.org/ScholarlyArticle)
