# tiny-team 自我迭代任务书

## 背景

当前内置插件：`read` / `write` / `list_dir` / `shell` / `search` / `calculator` / `create_tool`。

最大短板：`write` 只能整文件覆盖，缺少**精确局部修改**能力。模型改几行代码时被迫重写整文件，易出错、费 token。

## 目标（按优先级）

### P0 — 必须完成：新增 `edit` 插件

在 `plugins.go` 中实现 `edit`（str_replace 风格）：

**参数：**
- `path` (string, required)：工作区内相对路径
- `old_str` (string, required)：要被替换的原文
- `new_str` (string, required)：替换后的新文

**行为约定：**
1. 使用现有 `resolvePath` 做沙箱路径解析，禁止逃逸
2. 读取文件全文（可参考 `read` 的大小上限逻辑，或至少支持常见源码体积）
3. `old_str` 在文件中必须**恰好出现一次**：
   - 0 次 → 返回明确错误（含 path）
   - ≥2 次 → 返回明确错误，提示匹配不唯一，要求提供更长上下文
4. 替换成功后写回文件，返回简短确认，例如：`edited path (replaced N bytes with M bytes)`
5. 不要做 regex，做**字面量**替换

**注册：**
- 在 `tools.go` 的 `DefaultRegistry` 中注册 `NewEditTool(workspace)`（建议放在 `write` 之后）

**测试：**
- 在 `plugins_test.go` 增加至少覆盖：
  - 成功替换一次
  - old_str 不存在 → error
  - old_str 出现两次 → error
  - 路径逃逸被拒绝

**文档：**
- 更新 `README.md` 内置插件表，加入 `edit` 一行说明

### P1 — 建议完成（有余力再做）

1. 更新 `main.go` 顶部注释里的插件列表，加上 `edit`
2. 若时间允许：给 `edit` 增加可选参数 `replace_all`（bool，默认 false）；仅当为 true 时允许替换全部匹配

### 明确不要做的事

- 不要引入新的第三方依赖
- 不要改动模型协议 / HTTP 层（`model.go`）
- 不要破坏现有测试：改完后必须 `go test ./...` 全部通过
- 不要删除或弱化 `create_tool`
- 工作区外的无关文件不要动

## 验收标准

1. `go test ./... -count=1` 全部通过
2. `DefaultRegistry` 中能拿到名为 `edit` 的工具
3. 手工场景：对某文件执行一次精确替换后，`read` 能看到新内容，且未改动未匹配部分
4. README 插件表已更新

## 推荐执行顺序

1. 阅读 `plugins.go`、`tools.go`、`plugins_test.go` 了解现有模式
2. 实现 `editTool`（Spec + Execute）
3. 注册到 `DefaultRegistry`
4. 补测试并运行 `go test ./...`
5. 更新 README / 注释
6. 用 `final_answer` 汇报：改了哪些文件、测试结果、简要设计说明
