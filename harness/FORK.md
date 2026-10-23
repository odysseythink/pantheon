# harness — odyBox 自家 agent 框架（eino 派生）

本目录是 [CloudWeGo eino](https://github.com/cloudwego/eino) 的派生分支，作为
`github.com/odysseythink/pantheon/harness` 并入 pantheon 模块，供 odyBox 后端直接引用。
本文档即 Apache-2.0 §4(b) 要求的「修改声明」。

## 基线

- 上游：`github.com/cloudwego/eino`
- 快照时间：2026-10-19
- **精确基线：`v0.9.22` = `0a5ca53b9d3bf7b661dccc863465ec568a421eea`**
  （feat(schema): support provider file references in media messages, 2026-10-19）
  与 v0.9.21 的差异即 v0.9.22 全量：`schema.Message.FileID` 媒体输入、
  `adk/middlewares/{reduction,patchtoolcalls}` 改动、`compose/agentic_tools_node`、
  `adk/wrappers.go`；go.mod 依赖集与 v0.9.21 一致。
- **diff 校准（2026-10-23）**：从上游 `v0.9.22` 重新导出快照，将本目录 Go 文件的
  导入路径反替换回 `github.com/cloudwego/eino` 后与上游**逐字节一致**
  （差异仅剩已声明项：go.mod/go.sum 删除、doc.go 包注释、README 等元数据与
  `scripts/`/`examples/`/`ext/` 附属目录删除）。后续同步上游可直接以此为基线
  做三方向 diff。

## 并入 pantheon 的改动（2026-10-23）

1. **删除 `go.mod` / `go.sum`**，依赖迁入 `../go.mod`（由 `go mod tidy` 解析）。
   本目录自此不再是独立 module，包路径为 `github.com/odysseythink/pantheon/harness/...`。
2. **导入路径本地化**：全仓 `github.com/cloudwego/eino` →
   `github.com/odysseythink/pantheon/harness`，共 **784 处 / 272 个文件**
   （本目录 732 处，odyBox `backend/` 52 处）。113 处为 `internal/...` 自引用。
3. `doc.go` 的包名由 `eino` 改为 `harness`，包注释重写。
4. 删除上游独立仓库的元数据与附属目录：`README.md`、`README.zh_CN.md`、
   `llms.txt`、`CONTRIBUTING.md`、`CODE_OF_CONDUCT.md`、`.golangci.yaml`、
   `.licenserc.yaml`、`_typos.toml`、`scripts/`、`examples/`、`ext/`。
   `.gitignore` 精简为 Go 项目通用规则。
5. 保留 `LICENSE-APACHE` 与每个源文件头部的 Apache-2.0 版权声明，未改动。

## 许可

Apache License 2.0（见 `LICENSE-APACHE`）。分发衍生版本时须保留许可与版权声明，
并标注修改——本文档承担标注职责。上游版权归 CloudWeGo Authors 所有。

## 同步上游

若需要跟进上游 eino 的新版本：

1. 取上游目标 tag 的源码快照，与本目录做 diff（建议先切到基线 commit 校准，见上文）。
2. 只把**新增/变更文件**合入，避免整体覆盖。
3. 对合入文件应用导入路径替换（长路径优先，一次性 sed）：

   ```bash
   # 在 pantheon/ 下执行
   grep -rl 'github.com/cloudwego/eino' harness --include='*.go' \
     | xargs sed -i '' 's|github.com/cloudwego/eino|github.com/odysseythink/pantheon/harness|g'
   ```

4. `cd .. && go mod tidy && go build ./... && go test ./harness/...`

注意：上游新增的包若引入新依赖，需在 `../go.mod` 中由 tidy 补齐；上游
`internal/` 边界在本仓仍受 Go 的 internal 规则约束（仅 `harness/**` 可引用）。

## 本地偏离记录

按时间倒序，每次对上游逻辑的实质修改都追加一条（纯导入/格式化改动不必记录）。

- 2026-10-23：并入 pantheon 模块，导入路径本地化（见上）。**逻辑零改动**。
