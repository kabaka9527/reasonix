# Reasonix Plugin Packages

Reasonix plugin packages bundle skills, hooks, and MCP servers behind one
installable unit.

## Install

```bash
reasonix plugin install git:github.com/obra/superpowers --yes
```

Preview without writing files:

```bash
reasonix plugin install git:github.com/obra/superpowers --dry-run
```

Local development:

```bash
reasonix plugin install /path/to/plugin --link --yes
```

Installed plugin state is stored in:

```text
~/.reasonix/plugin-packages.json
~/.reasonix/plugins/<name>/
```

## Native Manifest

Reasonix plugins can declare `reasonix-plugin.json` at the plugin root:

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

Relative paths are resolved inside the plugin root. Reasonix does not run
third-party install scripts during plugin installation.

## Codex Compatibility

Reasonix also reads Codex plugin manifests at `.codex-plugin/plugin.json`.
For packages such as Superpowers, Reasonix maps:

- `skills` to Reasonix skill roots.
- `hooks/session-start-codex` to the Reasonix `SessionStart` hook when present.

Plugin hooks receive these environment variables:

- `REASONIX_PLUGIN_ROOT`
- `REASONIX_PLUGIN_NAME`
- `REASONIX_PLUGIN_VERSION`
- `REASONIX_HOME`
- `REASONIX_WORKSPACE_ROOT`

## Management

```bash
reasonix plugin list
reasonix plugin show superpowers
reasonix plugin doctor superpowers
reasonix plugin disable superpowers
reasonix plugin enable superpowers
reasonix plugin remove superpowers --yes
```

Desktop exposes the same backend operations through Wails methods:

- `Plugins`
- `PlanPluginInstall`
- `InstallPlugin`
- `RemovePlugin`
- `SetPluginEnabled`
- `UpdatePlugin`
- `PluginDoctor`
