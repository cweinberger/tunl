# TUNL

A cyberpunk TUI for managing SSH local port forwards. Built with [Bubbletea](https://github.com/charmbracelet/bubbletea) + [Lipgloss](https://github.com/charmbracelet/lipgloss).

<!-- ![tunl screenshot](screenshot.png) -->

## Features

- **Port forward management** -- Create, kill, and rename SSH `-L` tunnels
- **Named tunnels** -- Label tunnels for easy identification (`3030=rfx-engine`)
- **Well-known port detection** -- Auto-names common ports (postgres, redis, mysql, etc.)
- **Multi-host support** -- Per-tunnel host override (`3030@server1=api`)
- **Auto-detect existing tunnels** -- Picks up running `ssh -L` processes on startup
- **Persistent state** -- Tunnel names survive restarts via `~/.tunl.json`
- **Recent hosts** -- Cycle through previously used hosts when creating tunnels
- **Quick open** -- Open `http://localhost:<port>` in browser with a keypress
- **Keyboard-driven** -- Full arrow key navigation + shortcut keys for everything
- **Cyberpunk UI** -- Gradient neon logo, styled tunnel list, and a random quote on every launch

## Installation

### From source

```bash
go install github.com/cweinberger/tunl@latest
```

### Manual build

```bash
git clone https://github.com/cweinberger/tunl.git
cd tunl
go build -o tunl .
```

### Dependencies

- **Go 1.21+**
- **ssh**

## Usage

```bash
# TUI only, no initial tunnels
tunl

# Open a tunnel and launch TUI
tunl --host user@remote 3025

# Multiple tunnels with names
tunl --host user@remote 3030=rfx-engine 3025=bridge

# Different local/remote ports
tunl --host user@remote 3025:8080=api

# Per-tunnel host override
tunl 3030@server1=api 5432@server2=db

# Full spec format
# port[:remoteport][@host][=name]
tunl 3030:8080@user@server=api
```

### Recommended: set a default host

```bash
export TUNL_DEFAULT_HOST="user@remote"
# then just:
tunl 3025 5432
```

Or add an alias:

```bash
alias tn="tunl --host user@my-server"
```

## Controls

| Key | Action |
|-----|--------|
| `Up/Down` | Navigate tunnels and menu |
| `Enter/o` | Open tunnel URL in browser |
| `1-9` | Quick-open tunnel by number |
| `n` | Create new tunnel |
| `e` | Rename selected tunnel |
| `x` | Kill selected tunnel |
| `K` | Kill all tunnels |
| `r` | Refresh (detect existing tunnels) |
| `q` | Quit menu |

### Add tunnel form

| Key | Action |
|-----|--------|
| `Tab/Shift+Tab` | Navigate fields |
| `Ctrl+J/K` | Cycle through recent hosts |
| `Enter` | Connect |
| `Esc` | Cancel |

## Port spec format

```
port                       Local = remote, default host
port:remoteport            Different local/remote ports
port@host                  Per-tunnel host override
port:remoteport@host       Remote port + host override
port=name                  Named tunnel
port:remoteport@host=name  Full spec
```

## Well-known ports

Tunnels to these remote ports are auto-named if no explicit name is given:

| Port | Name |
|------|------|
| 3000 | dev-server |
| 3306 | mysql |
| 5432 | postgres |
| 5672 | rabbitmq |
| 6379 | redis |
| 8080 | http-alt |
| 9090 | prometheus |
| 9200 | elasticsearch |
| 27017 | mongo |

## How it works

tunl wraps `ssh -L` to create local port forwards, presenting them in an interactive TUI. It auto-detects existing `ssh -L` processes via `ps`, so tunnels created outside tunl are picked up too. Tunnel metadata (names, ports, hosts) is persisted to `~/.tunl.json` so names survive across restarts.

When you "just quit" (without killing), tunnels keep running in the background. Next time you launch tunl, they'll be detected and restored with their names.

## License

MIT
