# embyfin-mcp

[![GitHub release](https://img.shields.io/github/v/release/katbyte/embyfin-mcp?color=blueviolet)](https://github.com/katbyte/embyfin-mcp/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/embyfin-mcp?color=00ADD8)](https://github.com/katbyte/embyfin-mcp/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/embyfin-mcp?color=blue)](https://github.com/katbyte/embyfin-mcp/blob/main/LICENSE)
![build](https://github.com/katbyte/embyfin-mcp/actions/workflows/build.yaml/badge.svg)
![test](https://github.com/katbyte/embyfin-mcp/actions/workflows/pr-tests.yaml/badge.svg)
![lint](https://github.com/katbyte/embyfin-mcp/actions/workflows/pr-golangci-lint.yaml/badge.svg)

An MCP server (and CLI) for curating [Emby](https://emby.media) and [Jellyfin](https://jellyfin.org)
media libraries — search, inspect, audit, and fix metadata matches from an AI client such as Claude Code.

The design principle: **detection is code, correction is judgment.** The server runs cheap
deterministic checks over the whole library and produces worklists; the AI reasons only about
the anomalies.

## Installation

```bash
go install github.com/katbyte/embyfin-mcp@latest
```

## Configuration

All options can be passed as command-line flags, environment variables, or via a configuration file.

| Variable | Flag | Description |
|---|---|---|
| `EMBYFIN_BACKEND` | `--backend`, `-b` | `emby` (default) or `jellyfin` |
| `EMBYFIN_SERVER` | `--server`, `-s` | media server URL, e.g. `http://nas:8096` |
| `EMBYFIN_TOKEN` | `--token`, `-t` | API key (server dashboard → Advanced → API Keys) |
| `EMBYFIN_LOG` | | log level (`WARN` default; `DEBUG`, `TRACE`, ...) |
| `EMBYFIN_LISTEN` | `--listen` | serve MCP over HTTP on this address (e.g. `:8080`) instead of stdio |
| `EMBYFIN_AUTH_TOKEN` | `--auth-token` | bearer token required on the HTTP endpoint |

### Configuration File

You can place a `.embyfin-mcp` file in your home directory `~/.embyfin-mcp` (for global settings)
or in your current directory `./.embyfin-mcp` (for per-project settings). Keys match the long flag
names using the `env` format:

```env
BACKEND=emby
SERVER=http://nas:8096
TOKEN=ey...
```

## Usage

Quick connectivity check:

```bash
embyfin-mcp info
```

### Register with Claude Code

`.mcp.json`:

```json
{
  "mcpServers": {
    "emby": {
      "command": "embyfin-mcp",
      "args": ["serve"],
      "env": {
        "EMBYFIN_SERVER": "http://nas:8096",
        "EMBYFIN_TOKEN": "..."
      }
    }
  }
}
```

### Run as a service (HTTP transport)

`serve --listen :8080` serves the MCP Streamable HTTP transport at `/mcp` (plus `GET /healthz`)
instead of stdio. Set `EMBYFIN_AUTH_TOKEN` so clients must send `Authorization: Bearer <token>`;
without it anyone who can reach the port can use every tool. Register it from any machine:

```bash
claude mcp add --transport http embyfin http://nas:8080/mcp \
  --header "Authorization: Bearer $EMBYFIN_AUTH_TOKEN"
```

### Docker

Releases publish a multi-arch (amd64, arm64) image to `ghcr.io/katbyte/embyfin-mcp`, tagged
`vX.Y.Z`, `vX.Y` and `latest`. `docker-compose.yml` is the default always-on deployment: it runs
that image and reads secrets from a gitignored `.env` (copy `.env.example`). Adjust
`EMBYFIN_SERVER`, `EMBYFIN_BACKEND` and `TZ` in the compose file, then:

```bash
cp .env.example .env      # fill in EMBYFIN_TOKEN and EMBYFIN_AUTH_TOKEN
docker compose up -d
```

`make docker` builds the same image from source, tagged `embyfin-mcp`, with version info from
git. The image is alpine-based (so `docker exec -it embyfin-mcp sh` works), runs as a non-root
user and has a healthcheck against `/healthz`. The binary is the entrypoint, so `docker run --rm
ghcr.io/katbyte/embyfin-mcp info` works as a connectivity check with the `EMBYFIN_*` variables
passed via `-e`.

## MCP Tools

Tools are named resource-first (`library_*`, `item_*`, `session_*`...) so they group by what
they act on. Tools that change server state say so in their descriptions. Timeframe-taking
tools default to the last 60 days.

| Resource | Tools |
|---|---|
| server | `server_info`, `server_stats`, `server_activity`, `server_devices`, `server_logs`, `server_log` |
| tasks | `task_list`, `task_run` |
| libraries | `library_list`, `library_get`, `library_search` (filters: library/genre/year/person), `library_recent`, `library_genres`, `library_people`, `library_scan` |
| audits | `library_audit_missing_metadata_provider`, `library_audit_missing_poster`, `library_audit_missing_overview`, `library_audit_year_mismatch`, `library_duplicates` |
| items | `item_get`, `item_find_by_metadata_id`, `item_similar`, `item_refresh`, `item_edit`, `item_instant_mix`, `item_last_watched`, `item_watch_history`, `item_set_watched`, `item_set_favourite` |
| identify | `item_identify` (suggestions), `item_identify_apply` |
| artwork | `item_artwork`, `item_artwork_set` |
| subtitles | `item_subtitle_search`, `item_subtitle_download` |
| shows | `show_seasons`, `show_episodes`, `show_missing` |
| users | `user_list`, `user_history`, `user_next_up`, `user_favourites` |
| sessions | `session_list`, `session_play`, `session_command`, `session_message` |
| playlists | `playlist_list`, `playlist_get`, `playlist_create`, `playlist_add`, `playlist_remove` |
| collections | `collection_list`, `collection_get`, `collection_create`, `collection_add`, `collection_remove` |

`item_delete` (permanently removes media files) is only registered when
`--enable-delete` / `EMBYFIN_ENABLE_DELETE` is set.

## Development

```bash
make            # fmt + build
make check-all  # build + test + all linters + depscheck
```

Dev tools are pinned in `.tools/go.mod` (actionlint in `.tools/actionlint/go.mod`) and built
into `.tools/bin` by make. On a noexec checkout point `TOOLS_BIN` somewhere local, e.g.
`make TOOLS_BIN=~/.cache/embyfin-mcp/bin lint`.
