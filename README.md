# tiny-team

<p align="center">
    <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/License-Apache_2.0-blue.svg"></a>
    <a href="https://go.dev"><img alt="Go" src="https://img.shields.io/badge/Go-1.22%2B-00ADD8.svg?logo=go&logoColor=white"></a>
    <a href="https://github.com/huggingface/smolagents"><img alt="inspired by smolagents" src="https://img.shields.io/badge/inspired%20by-smolagents-FFD21E.svg"></a>
    <a href="https://1parado.github.io/tiny-team/"><img alt="Docs" src="https://img.shields.io/badge/docs-GitHub%20Pages-0071e3.svg"></a>
</p>

<h3 align="center">
    Agents that call agents like tools — everything is a plugin
</h3>

<p align="center">
  <a href="https://1parado.github.io/tiny-team/"><b>项目介绍页 →</b></a>
</p>

Go 编写的迷你多智能体框架：管理者将子智能体当作工具调用，各成员独立运行 ReAct 循环。

**插件化工具系统**：内置 `read` / `write` / `list_dir` / `shell` / `search` / `calculator`，并提供元插件 `create_tool`，让模型在运行时为自己编写新的底层工具。

## 项目主页

介绍页（架构流程图 · 设计原理 · 快速开始）：

**https://1parado.github.io/tiny-team/**

## 实时轨迹 Web UI

使用 `-web :8765` 启动后，浏览器可查看运行中的 ReAct 时间线（委派、工具调用、token 与耗时）：

```bash
go run . -web :8765 -task "在 workspace 写 hello.txt 并读回来"
# 打开 http://localhost:8765
```

<p align="center">
  <img src="docs/trace-ui.svg" alt="运行轨迹 Web UI 预览" width="640" />
</p>

## 核心特性

- **Agent-as-Tool**：子智能体对 manager 表现为普通工具（单参数 `task`）
- **一切即插件**：`ToolRegistry` 统一管理工具，运行时可通过 `create_tool` 动态注册
- **沙箱工作区**：文件与 shell 操作限制在 `-workspace` 目录内
- **双协议**：OpenAI-compatible + Anthropic
- **Token 统计** + **实时轨迹 Web UI**

## 内置插件

| 插件 | 说明 |
|------|------|
| `read` | 读取工作区内文本文件 |
| `write` | 写入/创建文本文件 |
| `list_dir` | 列出目录内容 |
| `shell` | 在工作区内执行 shell 命令（30s 超时） |
| `search` | 工作区内递归文本搜索 |
| `calculator` | 简单四则运算 |
| **`create_tool`** | **元插件**：模型用 shell 模板为自己编写新工具 |
| `final_answer` | 结束 ReAct 循环 |

### create_tool 示例

模型可以这样给自己造一个数行数的工具：

```json
{
  "name": "count_lines",
  "description": "Count lines in a file",
  "parameters": {
    "type": "object",
    "properties": { "path": { "type": "string" } },
    "required": ["path"]
  },
  "command": "wc -l {{path}}"
}
```

注册成功后，后续步骤即可直接调用 `count_lines`。

## 快速开始

```bash
cp .env.example .env
# 编辑 .env 填入 API_KEY 和 MODEL

go run . -task "在 workspace 里写一个 hello.txt 并读回来"
# 可选：-workspace ./myws  -web :8765
```

## 运行测试

```bash
go test ./... -count=1
```

原 Eiffel Tower demo 已迁移到 `TestEiffelTowerDemo`。

## 参考

- [项目介绍页 (GitHub Pages)](https://1parado.github.io/tiny-team/)
- [smolagents](https://github.com/huggingface/smolagents)
- [tiktoken-go](https://github.com/pkoukk/tiktoken-go)
