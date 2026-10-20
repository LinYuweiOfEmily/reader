# 今日论文推荐实施计划

**Goal:** 在论文库提供手动触发的今日论文推荐：偏好保存、实时检索、AI 筛选、历史去重、一键导入。

**Architecture:** Go scholar 检索真实元数据，Reader 使用当前 AI 配置一次性筛选候选，SQLite settings 保存偏好及最近 30 次推荐。UI 使用现有 shadcn 控件与 importReference。AI 只能选择候选 ID，不得生成题名、链接或引用；评分由四项分数计算，摘要级证据。网络错误、AI 失败或取消均不得保存不完整推荐。

**Tech Stack:** Go/SQLite、React/TypeScript、现有 API client/OpenAPI。

用户已批准范围：3–5 篇；默认 LLM RL/mismatch/加速画像可编辑；排除已读，标记已入库和曾推荐；无定时任务，无完整分步精读/归档流程。

## 数据契约

GET /api/papers/recommendations → `{ preferences, history }`
PUT /api/papers/recommendations/preferences，body preferences → preferences
POST /api/papers/recommendations，body `{}` → RecommendationRun；请求取消即中止后台。运行中再次触发返回 409。

Preferences: `{ interests: string, queries: string[], count: number }`，count 3..5。
Run: `{ id, createdAt, provider, preferences, warnings: string[], papers: RecommendedPaper[] }`。
Paper: `{ id, title, authors: string[], date, status, url, doi, arxiv, abstract, priority: "high"|"medium"|"low", score, scores: { relevance,inspiration,evidence,novelty }, reason, limitation, previousRecommendation: boolean, documentId?: string }`。
评分 0..10，40/25/20/15 加权后保留一位小数。status 为来源能支持的中文状态，不能假定会议录用。bibliography 来自检索，不接受模型改写。历史再次读取时更新 documentId/已读过滤状态不删除原推荐记录。

## 工作项
- [x] scholar：实现 SearchRecommendations(ctx, queries, limit) ([]Metadata, []string, error)，来源校验、限量、去重、失败回退、fixture tests。
- [x] Reader 后端：偏好校验、任务取消、库内已读去重、稳定候选 ID、严格 AI JSON 验证、原子历史保存、HTTP tests。
- [x] UI：今日推荐对话框，偏好、生成/取消、历史、评分和理由、来源、导入/已入库；API client 和共享类型。
- [x] 更新 OpenAPI 并生成 schema，集成前后端。
- [x] 针对性测试、typecheck、test、build；独立审查、按逻辑提交。保留已知 Windows 全量测试失败记录。

一次推荐只有一个 AI 排序调用，不维持跨日期的后台会话。数据库推荐历史独立于模型会话。后续若增加多轮推荐细化，按任务复用会话。

## 验证结果

- Scholar fixture tests 通过；Reader 推荐 HTTP/持久化/候选校验测试及既有 ZCode 复用回归通过；推荐界面 6 个生命周期/导入测试通过。
- `pnpm typecheck`、最终 `pnpm build` 通过。规格与代码审查发现的历史已读刷新、无身份候选边界、保存阶段取消/导航竞态均已修复。
- `pnpm test` 未通过：既有 Windows 日志脱敏断言 `TestRedactionAndFullExport` 和 Agent 流式测试 `TestAgentStreamingBeforeCompletion` 失败；90 秒 Go 测试上限中断了全量执行，后续全量 Vitest 未执行。新界面测试独立执行通过。
- 真实检索＋ZCode 的 opt-in 测试未成功，未进入 AI 调用：当前 Reader 手动代理和 Windows 系统代理都是 `127.0.0.1:7897`；独立 curl 访问 arXiv/Semantic Scholar 均 TLS 握手失败，S2 直连超时。保留错误详情，不声称真实推荐已验证。未改用户代理配置或重启正在使用的 Reader。
- 推荐记录最多 30 次，关闭/取消不留下长驻模型会话。外部 ZCode 日志不由 Reader 删除。
