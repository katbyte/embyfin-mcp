# Changelog

## unreleased

- initial project scaffold: cobra/viper CLI (`serve`, `info`, `version`), MCP server over stdio
- shared MediaBrowser API client (`lib/embyfin`) supporting both Emby and Jellyfin backends
- 57 MCP tools (resource-first naming) covering server ops, libraries, audits, items,
  identify, artwork, subtitles, shows, users, live sessions, playlists, and collections
- `item_delete` guarded behind `--enable-delete` / `EMBYFIN_ENABLE_DELETE`
- timeframe-taking tools (`server_activity`, `library_recent`, `user_history`,
  `item_watch_history`) default to the last 60 days
- `serve --listen` / `EMBYFIN_LISTEN` serves the Streamable HTTP transport at `/mcp` (with
  `GET /healthz`); `EMBYFIN_AUTH_TOKEN` requires a bearer token on it
- Dockerfile (alpine, non-root, healthcheck, HTTP transport by default), `docker-compose.yml` and
  `make docker`; releases push a multi-arch image to `ghcr.io/katbyte/embyfin-mcp`
- fixes from the first run against a real Emby 4.10 server: `user_next_up` (needs
  `LegacyNextUp`), resume list (needs `Recursive`/`MediaTypes`), `item_similar` (needs a user),
  `item_identify` with no name override, `library_search` defaulting to the library's kind,
  `show_missing` (Emby ignores `IsMissing`; filter on path-less episodes), `user_history`
  rebuilt on the activity log (list endpoints omit `LastPlayedDate`), `server_logs` field
  names, and empty collections now serialise as `[]` instead of `null`
