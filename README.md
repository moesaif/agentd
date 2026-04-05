# agentd

> Your AI agent that acts. Not one you talk to.

A single binary that runs in your terminal, watches your dev environment, and does things without being asked.

```sh
curl -fsSL https://get.agentd.dev | sh && agentd init
```

<!-- TODO: Replace with terminal recording GIF -->
<!-- Demo: agentd start → GitHub Action fails → agentd opens issue with root cause analysis -->

---

## Why agentd?

Most AI dev tools are chatbots — you talk to them, they respond. **agentd is the opposite.** It watches your environment and acts proactively.

|  | agentd | OpenClaw | n8n | Zapier |
|---|---|---|---|---|
| **Size** | ~17MB binary | 500MB+ Docker stack | Docker compose | Cloud only |
| **Setup** | `curl \| sh` | Docker, dashboard, config | Docker, web UI | Web signup |
| **Config** | 5 lines YAML | JSON + dashboard | Visual editor | Visual editor |
| **Skills** | Any script (bash, Python, JS) | TypeScript modules | Node modules | No code |
| **LLM** | Any provider | OpenAI only | Varies | Built-in |
| **Proactive** | Yes — watches and acts | No — you message it | Trigger-based | Trigger-based |
| **MCP** | Built-in | No | No | No |
| **Offline** | Yes (with Ollama) | No | Partial | No |

---

## Quick Start

```sh
# Install
curl -fsSL https://get.agentd.dev | sh

# Setup (detects API keys from env, installs bundled skills)
agentd init

# Run
agentd start
```

Set your LLM provider:

```sh
# Anthropic (recommended)
export ANTHROPIC_API_KEY=sk-ant-...

# Or OpenAI
export OPENAI_API_KEY=sk-...

# Or use local models with Ollama
# Set provider: ollama and model: llama3 in ~/.agentd/config.yaml
```

---

## How It Works

agentd runs a simple event loop:

```
[Watchers] → [Events] → [Match Skills] → [Run Script] → [LLM] → [Actions]
```

1. **Watchers** observe your environment (filesystem, git, webhooks, cron)
2. Events are matched against **skill triggers**
3. Matching **skills** (plain scripts) execute and produce context
4. The **LLM** decides what action to take
5. **Actions** execute (shell commands, HTTP calls, notifications)

Everything is logged to SQLite for history and debugging.

---

## Skills

Skills are plain scripts in any language with a YAML frontmatter block. Drop them in `~/.agentd/skills/`.

```bash
#!/bin/bash
# ---
# name: git-pr-summary
# description: When a PR is opened, summarize the diff and post a comment
# triggers:
#   - webhook: github.pull_request
# env:
#   - GITHUB_TOKEN
# ---

PR_NUM=$(echo "$AGENTD_EVENT" | jq -r '.number')
REPO=$(echo "$AGENTD_EVENT" | jq -r '.repository.full_name')

# Fetch diff and output it — agentd sends this to the LLM
curl -sL -H "Authorization: token $GITHUB_TOKEN" \
  "https://api.github.com/repos/$REPO/pulls/$PR_NUM" | jq '.diff_url'
```

### Trigger Types

```yaml
triggers:
  - git: push                      # git push in watched repo
  - git: commit                    # new commit
  - filesystem: "*.go"             # file matching pattern changed
  - webhook: github.push           # inbound webhook event
  - webhook: any                   # any webhook
  - cron: "*/15 * * * *"          # every 15 minutes
  - cron: "@startup"               # on agentd start
  - cron: "@hourly"
```

### Bundled Skills

| Skill | Trigger | What it does |
|-------|---------|-------------|
| `git-pr-summary` | PR opened | Summarizes diff, suggests reviewers |
| `failing-action-triage` | CI failure | Fetches logs, identifies root cause |
| `todo-issue-sync` | File changed | Finds new TODOs, reports them |
| `daily-standup` | Weekdays 9am | Generates standup notes from git |
| `meeting-prep` | Every hour | Checks calendar, prepares context |

---

## MCP Integration

agentd exposes an MCP server so Claude Code, Cursor, and other AI editors can interact with it.

Add to your `.vscode/mcp.json`:

```json
{
  "mcpServers": {
    "agentd": {
      "url": "http://localhost:7778/mcp"
    }
  }
}
```

Available tools:
- `agentd_list_skills` — list all loaded skills
- `agentd_trigger_skill` — manually run a skill
- `agentd_get_history` — recent events and actions
- `agentd_set_memory` / `agentd_get_memory` — persistent key-value store
- `agentd_run` — run an arbitrary prompt against the agent

---

## Config

`~/.agentd/config.yaml` — minimal by design:

```yaml
llm:
  provider: anthropic
  api_key: ${ANTHROPIC_API_KEY}
  model: claude-sonnet-4-5

watchers:
  git: true
  filesystem: true
  webhook:
    enabled: true
    port: 7777
  cron:
    enabled: true

mcp:
  enabled: true
  port: 7778
```

---

## CLI

```
agentd start              # start (foreground)
agentd start -d           # start as daemon
agentd stop               # stop daemon
agentd status             # show status + recent events
agentd skills             # list loaded skills
agentd skills run <name>  # manually trigger a skill
agentd history            # show event/action history
agentd memory get <key>   # read persistent memory
agentd memory set <k> <v> # write persistent memory
agentd logs               # tail the log
agentd mcp                # show MCP connection info
agentd init               # first-run setup wizard
```

---

## Architecture

```
agentd/
├── cmd/agentd/main.go       # CLI entry point (Cobra)
├── internal/
│   ├── agent/                # Core event loop + LLM orchestration
│   ├── config/               # YAML config loader
│   ├── db/                   # SQLite state (events, actions, memory)
│   ├── llm/                  # Provider-agnostic LLM client
│   │   ├── openai.go         # OpenAI + compatible (Groq, Together, Ollama)
│   │   └── anthropic.go      # Anthropic native API
│   ├── mcp/                  # MCP JSON-RPC server
│   ├── skills/               # Skill loader, manifest parser, runner
│   └── watchers/             # Filesystem, git, webhook, cron watchers
├── skills/                   # Bundled skills
├── Makefile
└── install.sh
```

**Tech stack:** Go, SQLite (pure Go via modernc.org/sqlite), Cobra, fsnotify, robfig/cron, charmbracelet/log.

**Zero CGO.** Single static binary. Cross-compiles to Linux, macOS, Windows on amd64 and arm64.

---

## Roadmap

- [ ] Skill marketplace (submit PRs to `/skills`)
- [ ] `agentd sync` — share skills across machines via git
- [ ] Web UI (optional, off by default)
- [ ] Mobile push notifications
- [ ] `agentd cloud` — managed hosting
- [ ] Streaming LLM responses
- [ ] Skill chaining (output of one skill feeds another)
- [ ] `agentd watch <dir>` — watch additional directories

---

## Contributing

```sh
git clone https://github.com/moesaif/agentd
cd agentd
make build
./bin/agentd init
./bin/agentd start
```

Skills are the easiest way to contribute. Write a script, add frontmatter, submit a PR to `/skills`.

---

## License

MIT
