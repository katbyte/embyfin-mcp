# Changelog

## Unreleased

**New**

- Comparing a download folder or vault against a big TV library without a call per series: `library_episodes`, `library_export`, `show_episodes_exist`, `show_resolve`, `plan_check` and `quality_compare`.
- Audits: `audit_file_path`, `audit_provider`, `audit_language`, `audit_duplicate_episodes`, `audit_duplicate_series`, `audit_disc_folders`, `audit_anime_ids` and `audit_orphans` (with `item_orphans_delete`). `audit_all` has a row for every audit.
- `audit_missing_episodes` and `show_missing` can read a series' whole run from TMDB, and say when they cannot tell rather than answering "nothing missing".
- Quality rows carry frame rate, HDR, aspect ratio and display width, which give away upscales and anamorphic rips.
- `user_get` reports playback preferences; `server_info` reports its own version and the SDK's.
- `lib/tmdb` is generated from TMDB's OpenAPI document like the Emby and Jellyfin SDKs, and `make spec-refresh` re-vendors all three.

**Changed**

- Tools that did the same thing are merged: `library_items` searches and filters, `item_set_state` sets watched, favourite and position, `item_edit` takes one id or many. 89 tools: 62 read, 22 write, 5 delete.
- `playlist_delete` and `collection_delete` need `--enable-delete`.
- `library_export` only ever writes a new file.
- Breaking: sizes are in bytes and times in seconds everywhere, audio tracks are objects rather than strings, every list pages with `limit` and `offset`, and every audit answers `items_scanned` and `total_findings`.
- The TMDB setting is `--tmdb-token` / `EMBYFIN_TMDB_TOKEN`; `--tmdb-key` still works.

**Fixed**

- Emby finding nothing no longer ends the session, and a tool that fails unexpectedly returns an error.
- Answers that were confidently wrong: a path read as free that was taken, a film and a series reported as duplicates, unprobed files counted as having no audio, a name resolved to the wrong show, one user's plays credited to another, a subtitle download reported that never happened.
- Playlists: adding a series or album no longer doubles it, a move keeps repeated items, and a playlist takes only what its user can see.
- Deletes say what they remove: `item_delete` lists every path (a film alone in its folder takes the folder) and previews without `confirm`, and a Jellyfin collection no longer comes back after a delete.
- Identifying an item in a library with its fetchers off only sets its ids; Jellyfin wiped a series' episode numbers.
- Emby's merged versions of a film are versions, not duplicates.
- Names in any script are compared, and the history tools read their whole period.
- An older TMDB key no longer shows in errors, and the Emby key is not sent to another host on a redirect.
- Two-word settings in a `.embyfin-mcp` file (`TMDB_TOKEN`, `READ_ONLY`...) are read.

## 0.1.1 (2026-09-15)

No code changes. 0.1.0 published its binaries and image but not the Homebrew formula, because the tap token was not set; this release runs that path with the token in place.

## 0.1.0 (2026-09-15)

Initial release.

- **81 tools** for Emby and Jellyfin, in toolsets: 12 audits, identify, artwork, subtitles, metadata edits, watch state, playlists, collections, sessions and server admin. `core` (7 read-only tools) is the default; `--toolsets`, `--allow-tools`/`--deny-tools`, `--read-only` and `--enable-delete` decide the rest.
- **CLI**: `serve`, `info`, `tools`, `version`. Flags, `EMBYFIN_*` environment variables or a `.embyfin-mcp` file.
- **Transports**: stdio, or Streamable HTTP with `--listen` (bearer token required). Alpine Docker image at `ghcr.io/katbyte/embyfin-mcp`, plus `docker-compose.yml`.
- **Two generated Go SDKs**, `lib/emby` (Emby 4.10, 499 operations) and `lib/jf` (Jellyfin 12.0, 346), written by `internal/pandorest` from the servers' own OpenAPI documents, over a shared base client. `lib/embyfin` is the thin layer that makes both servers answer alike.
- **Tested against real servers**: unit tests, an SDK suite and a tool suite against Emby and Jellyfin in Docker, with provider calls recorded and replayed. Every registered tool must have a test, every GET is swept, and `make cover` merges the lot into the coverage badge.
