---
name: code-runner
description: 在隔离的 Docker Workspace 中编写、运行和检查 Go、Python 或 Shell 代码；当用户要求执行代码、验证脚本或生成文件时使用。
compatibility: 需要启用 LuckyClaw Docker sandbox。
---

# Code Runner

1. 使用 `write_file` 把代码写入当前 Workspace。
2. 使用 `exec` 在 `/workspace` 中运行或测试代码。
3. 使用 `read_file` 检查文本结果，使用 `list_dir` 查看生成文件。
4. 不要尝试访问 Workspace 之外的宿主路径；需要联网时先确认 Agent 的沙箱网络配置允许访问。
