# 自我进化任务书：`doctor` + 进化日记

## 背景

tiny-team 已具备 `read` / `write` / `edit` / `shell` / `create_tool` 等能力，但缺少：

1. **启动前自检**：环境、工具目录、工作区是否健康，一目了然。
2. **进化可追溯**：自我修改成功后没有统一的「进化日记」。

本任务要求 harness **自己改自己的源码**，落地这两项能力。

## 总目标

1. 新增内置插件 **`doctor`**（只读自检）。
2. 新增内置插件 **`log_evolution`**（向进化日记追加一条记录）；并约定危险改动成功后可调用它。
3. 注册进 `DefaultRegistry`，补测试，更新 `README.md`。
4. 全量 `go test ./... -count=1` 必须通过。

---

## 目标一：`doctor` 插件

### 参数

无必填参数。可选：

| 参数 | 类型 | 说明 |
|------|------|------|
| `verbose` | bool | 默认 false；为 true 时输出更完整的工具列表与检查细节 |

### 行为

在 **workspace 沙箱内** 与进程环境上做只读检查，返回一段人类可读的纯文本报告（不要 JSON 包一层，便于模型直接读）。报告至少包含：

1. **时间**：UTC 或本地时间一行。
2. **Workspace**：绝对路径、是否存在、是否可写。
3. **环境变量（只报有无，禁止打印密钥值）**：`API_KEY` / `MODEL`：set / missing。
4. **工具目录**：已注册工具名。
5. **Go 工具链（可选）**：`go version` 短超时。
6. **总结行**：`status: ok` 或 `status: degraded`。

### 注册

`DefaultRegistry` 最后注册 `NewDoctorTool(workspace, r)`。

### 测试

- `TestDoctorReportsWorkspaceAndTools`
- `TestDoctorDoesNotExposeSecrets`

---

## 目标二：进化日记 `log_evolution`

### 参数

| 参数 | 类型 | 说明 |
|------|------|------|
| `summary` | string | **必填** |
| `files` | string | 可选 |
| `tests` | string | 可选 |
| `detail` | string | 可选 |

### 行为

向 workspace 根 `EVOLUTION_LOG.md` 追加记录；不存在则创建并写标题 `# Evolution log`。

### 测试

- `TestLogEvolutionCreatesAndAppends`

---

## 验收标准

1. `go test ./... -count=1` 全部通过
2. `DefaultRegistry` 可 `Get("doctor")`、`Get("log_evolution")`
3. doctor 不泄露密钥
4. README 已更新
