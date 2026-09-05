# Changelog

## unreleased

- initial project scaffold: cobra/viper CLI (`serve`, `info`, `version`), MCP server over stdio
- shared MediaBrowser API client (`lib/embyfin`) supporting both Emby and Jellyfin backends
- 57 MCP tools (resource-first naming) covering server ops, libraries, audits, items,
  identify, artwork, subtitles, shows, users, live sessions, playlists, and collections
- `item_delete` guarded behind `--enable-delete` / `EMBYFIN_ENABLE_DELETE`
- timeframe-taking tools (`server_activity`, `library_recent`, `user_history`,
  `item_watch_history`) default to the last 60 days
