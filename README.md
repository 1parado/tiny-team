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

Go 编写的迷你多智能体框架：管理者将子智能体当作工具调用，各成员独立运行 ReAct 循环。

**插件化工具系统**：内置 `read` / `write` / `edit` / `list_dir` / `shell` / `search` / `calculator` / `doctor` / `log_evolution`，并提供元插件 `create_tool`。

## 实时轨迹 Web UI

```bash
# 交互模式：浏览器提交任务 / 中断
go run . -web :8765 -workspace ./workspace
# 打开 http://localhost:8765
```

## 内置插件

| 插件 | 说明 |
|------|------|
| `read` | 读取工作区内文本文件 |
| `write` | 写入/创建文本文件 |
| `edit` | 按字面量唯一匹配做局部替换（可选 replace_all） |
| `list_dir` | 列出目录内容 |
| `shell` | 在工作区内执行命令（30s；Windows 优先 pwsh → powershell → bash → cmd） |
| `search` | 工作区内递归文本搜索 |
| `calculator` | 简单四则运算 |
| **`create_tool`** | **元插件**：用 shell 模板注册新工具 |
| `doctor` | 只读自检（不泄露密钥） |
| `log_evolution` | 向 EVOLUTION_LOG.md 追加进化记录 |
| `final_answer` | 结束 ReAct 循环 |

## 快速开始

```bash
cp .env.example .env
go test ./... -count=1
go run . -task "列出 workspace 文件"
```

Windows 上 `shell` 会自动选用 PowerShell 7 (`pwsh`) 或 Windows PowerShell；也可用 Git Bash。
