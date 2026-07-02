# Reasonix 插件包

Reasonix 插件包把 skills、hooks 和 MCP servers 组织成一个可安装单元。

## 安装

```bash
reasonix plugin install git:github.com/obra/superpowers --yes
```

只预览计划，不写文件：

```bash
reasonix plugin install git:github.com/obra/superpowers --dry-run
```

本地开发：

```bash
reasonix plugin install /path/to/plugin --link --yes
```

插件状态和内容写入：

```text
~/.reasonix/plugin-packages.json
~/.reasonix/plugins/<name>/
```

## 原生 Manifest

Reasonix 原生插件在根目录声明 `reasonix-plugin.json`：

```json
{
  "name": "example",
  "version": "1.0.0",
  "description": "Example plugin",
  "skills": "skills",
  "hooks": {
    "SessionStart": [
      {
        "command": "hooks/session-start",
        "description": "Load startup context"
      }
    ]
  },
  "mcpServers": {
    "helper": {
      "command": "bin/helper"
    }
  }
}
```

相对路径都按插件根目录解析。Reasonix 安装插件时不会执行第三方安装脚本。

## Codex 兼容

Reasonix 也会读取 `.codex-plugin/plugin.json`。对于 Superpowers 这类插件，
Reasonix 会映射：

- `skills` 到 Reasonix skill root。
- 如果存在 `hooks/session-start-codex`，映射为 Reasonix `SessionStart` hook。

插件 hook 会收到这些环境变量：

- `REASONIX_PLUGIN_ROOT`
- `REASONIX_PLUGIN_NAME`
- `REASONIX_PLUGIN_VERSION`
- `REASONIX_HOME`
- `REASONIX_WORKSPACE_ROOT`

## 管理命令

```bash
reasonix plugin list
reasonix plugin show superpowers
reasonix plugin doctor superpowers
reasonix plugin disable superpowers
reasonix plugin enable superpowers
reasonix plugin remove superpowers --yes
```

Desktop 后端暴露同等 Wails 方法：

- `Plugins`
- `PlanPluginInstall`
- `InstallPlugin`
- `RemovePlugin`
- `SetPluginEnabled`
- `UpdatePlugin`
- `PluginDoctor`
