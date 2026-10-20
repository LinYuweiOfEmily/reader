# Windows ZCode

Reader 可通过 ZCode Desktop 自带的 Agent 后台运行文字问答、论文翻译和图片理解。

1. 安装 ZCode，在 ZCode 的模型设置中配置 API Key，并确认模型可聊天。
2. Reader「设置 → AI」选择 ZCode，运行可用性检测。
3. 从列表选择问答、翻译和图片模型（图片默认优先 Flash），保存设置。

桌面免费额度不等于独立后台接口的可用额度。当前接入使用 ZCode 自己加载的 API 模型配置；Reader 不读取密钥文件，也不把该 Key 复制到 Reader 的 API Agent。

自动发现支持 PATH、当前用户的 Programs/ZCode、C–Z 盘的 Program Files/ZCode。其他安装位置可在启动 Reader 前设置环境变量 `READER_ZCODE_HOME` 为包含 ZCode.exe 的目录。安装目录必须同时包含官方 `resources/glm/zcode.cjs` 和内置 provider 配置。

已适配 ZCode 0.16.9 的 ZCode Protocol v1。ZCode 升级后如果协议改变，Reader 会报告不兼容，不会把断流或半段结果当作成功。模型列表缓存最多五分钟；更改 ZCode 模型后可以重启 Reader 刷新。

问答与图片调用使用独立隔离工作目录；论文翻译由每个工作线程复用自己的隔离目录、后台进程和会话，成功批次之间不重新启动。失败、取消或模型切换会丢弃会话，后续调用重新创建；线程结束时关闭后台并清理目录。各线程不共享历史，线程内的历史由 ZCode 原生上下文管理处理。

关闭工具和 MCP，拒绝后台向 Reader 发出的操作请求。仅转发回答正文，成功结束且全文与流式内容一致后才允许保存；取消时发送 session/stop，超时终止后台进程。GLM-5.3 是文字模型；GLM-5.3-Flash 支持图片。图片模型独立选择，默认优先 Flash，检测必须通过随机色块挑战。ZCode 0.16.9 对 GLM-5.3 返回的图片支持标记不可靠，因此 Reader 明确拒绝向该文字模型发送图片。

## 开发验证

常规：`go test ./internal/reader -run '^TestZCode'`（apps/server 目录）。

会话复用：`go test ./internal/reader -run 'Test(Z[Cc]ode|TranslationZcode|ReusableZcode)'`。设置 `READER_TEST_ZCODE_REUSE_LIVE=1` 后运行 `TestTranslationZcodeLiveReuse`，会真实翻译两个单词并检查后台和会话保持一致，消耗 API 额度；耗时仅供诊断，不能视为整篇论文的提速基准。

真实验证需显式设置 `READER_TEST_ZCODE_LIVE=1`；它会调用已配置模型进行一段 JSONL 翻译、随机色块识别和后台取消验证，会消耗 API 额度。未设置时跳过真实调用。

手动验收：在 Reader 选择 ZCode 并通过文字、图片检测，翻译一段论文，确认中文逐步出现；发送问答后取消，确认不会保存半段成功回答。打包使用现有 `scripts/build-windows.ps1`，不需要重新下载依赖。

模型能力依据：[GLM-5.3](https://docs.z.ai/guides/llm/glm-5.3) 与 [GLM-5.3-Flash](https://docs.z.ai/guides/vlm/glm-5.3-flash) 官方文档。附件通过 ZCode 官方 kind/dataBase64 协议传入，模型选择在发送前完成。
