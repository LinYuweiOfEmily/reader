# Windows 本地构建

当前优化源码位于 `fix/codex-desktop-discovery` 分支。本地工作树为
`E:\agent_demo\reader\.worktrees\codex-discovery`，工具与缓存保存在
`E:\agent_demo\reader\work`；请保留这两个目录。安装包输出在工作树的 `release`。

在 PowerShell 中运行：

```powershell
Set-Location E:\agent_demo\reader\.worktrees\codex-discovery
./scripts/build-windows.ps1 -CacheRoot E:\agent_demo\reader\work
```

脚本会检查本地工具和资源校验和，使用冻结的 lockfile 离线安装依赖，运行类型检查、
Windows/Codex/诊断测试，然后完整编译、生成 NSIS EXE，并运行隔离的安装包组件测试。
最后一步使用临时书库，不修改现有 Reader 数据。失败会中止，不把缺失依赖静默改为联网下载。
这组检查不等同于全量测试；全量测试另用 `pnpm test`。

缓存包括 pnpm 10.30.3、Go 1.26.5、npm 包、Go modules、Electron 44.5.1 ZIP、
NSIS 3.0.4.1、NSIS resources 3.4.1 和 7-Zip。PDF 布局模型保存在
`apps/processor/models`。Electron、NSIS ZIP/7z 和模型在构建前校验 SHA256。
Node 使用本机 PATH 中的安装，当前验证版本为 24.14.0。

诊断 ZIP 的测试使用 Python 标准库独立验证 ZIP 内容和 CRC，Windows 默认执行 `python`；
也可设置 `READER_TEST_PYTHON` 为 Python 的完整路径。Reader 安装包运行不依赖 Python。
Windows 的链接排除测试使用目录 junction，不需要开启开发者模式或管理员权限。

修改业务代码且依赖版本不变时可复用缓存。升级依赖、Electron、Go、模型或打包工具时，
必须先补齐对应新缓存并更新校验/版本配置；不能保证新版本永久无需网络。
不要提交 `work`、`release`、`node_modules` 或二进制文件。

安装验证：退出旧 Reader 后运行 EXE，打开 Agent 设置，确认 Codex 显示已安装、已登录；
再打开一本文档发送问题，确认流式回复正常。此交互验收需在实际应用中手动完成。
