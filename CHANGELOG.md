# Changelog

## Unreleased

**New**

- `audit_file_path` checks a film is the film its file says it is. It compares the path with every title the film goes by (its name, original title, sort name and, with a TMDB token, TMDB's alternative titles) and takes a year either side as the same film. With a token it says which film a mismatched path names and whether the file's runtime backs it.
- `audit_file_path` reports a name spelled with a letter from another alphabet that only looks Latin ("Еden" with a Cyrillic Е, which a search for "Eden" never finds), and a film renamed by hand.
- `audit_file_path` and `audit_provider` take `ids`, to check a handful of items rather than a library.
- `item_get` lists every version of an item with its own file facts, and warns when a version's file names another film: two films matched to one id, which Emby merges into one. `audit_multiple_versions` and `audit_duplicates` mark such groups, and `quality_compare` adds a caveat.
- `item_get` and `library_items` show `original_title` when it differs from the name.
- `audit_all` counts a music library's missing album covers and genre spellings.
- `task_list` shows each task's id.

**Changed**

- `show_episodes` is gone: `library_episodes` takes the show by name or id (`series`) and a `season`, so one tool reads a show's episodes, a season's or a library's.
- `user_in_progress` is gone: `user_next_up` answers what to watch next and everything part way through (`in_progress`, was `resume`), with where each resumes. 87 tools: 60 read, 22 write, 5 delete.
- `--allow-tools` without `--toolsets` chooses from every tool, so `essential` loads all five. Beside `--toolsets`, naming a tool the sets don't hold is refused, naming the set to add.
- `item_refresh` waits for the refresh to land and says whether it did, so an edit made straight after is no longer undone.
- `item_identify_apply` sets every id the chosen title goes by when the library's fetchers are off. It warns when an nfo beside the file may bring the old title back and, on Emby, that watch state follows the ids.
- `item_delete`'s preview lists everything the server will take, including another item's nfo, subtitles and images whose names start with the same name.
- `item_artwork_set` says when Emby deletes the poster file beside the media.
- `audit_unwatched` and `item_last_watched` count watches by accounts that have since lost access to the library.
- `collection_delete` on Jellyfin watches a just-changed collection long enough for a slow refresh to come back, and says why.
- `audit_provider` pages in its own order, so every film is asked about once; the other paged lists break ties by date added.

**Fixed**

- `audit_file_path` no longer flags a film Jellyfin could not match, which it names after its folder, year and all ("Cube (1997)").
- A series name that only half-matches several shows is refused as a guess, rather than as a tie to narrow with `library`.
- Audits judged each version of an Emby film on its own: a 360p copy beside a 4K one was reported, a subtitle in another version didn't count, and an episode's two versions read as duplicates.
- `show_seasons`, `show_missing`, `show_episodes_exist` and `library_episodes` took a film's id as a show's and answered for an unrelated show. They now refuse it and say what the id is.
- An unknown id read as "nobody has watched this" or "nothing similar". `item_last_watched`, `item_similar`, `item_instant_mix`, `item_refresh`, `playlist_add` and `session_play` now say no item has it.
- On Emby, `user_next_up` lost in-progress films behind never-started episodes and never said when an item was last played, and `user_stats`' most played was always empty.
- Sort name edits on Emby were reported as saved and silently lost.
- `library_edit` could rename a library onto another library's name, and on Jellyfin answered a rename with no id.
- `library_items` ignored the sort when searching.
- `audit_quality` on Jellyfin missed a file it couldn't read.
- `plan_check` left out a size ratio or claim similarity of 0, the answer that matters most.
- `item_instant_mix` from a playlist returned nothing on Emby, and more tracks than the limit.
- `show_resolve` read a name of nothing but season, episode and encode markers as a title.
- `server_stats` counted no collections on Emby.
- Aspect ratios written as decimals ("1.5:1") are read.
- Nine Emby routes the SDK couldn't call now work: they need parameters Emby's own document doesn't declare.
- `show_missing` and `audit_missing_episodes` answered with another show's episodes when a series carried a film's ids, since TMDB numbers films and shows apart. They now say the ids disagree and suggest `item_identify`.
- `audit_missing_episodes` reported a show split across two library entries as each missing the other's episodes. Entries sharing ids are judged as one show, and the row says so.
- `show_episodes_exist` silently dropped the second copy of an episode held twice. It answers for the entry asked about and lists the rest in `other_copies`.
- `audit_quality`, `audit_runtime` and `audit_duplicate_episodes` judged a season's extras (a featurette Emby lists as an episode) as episodes.
- `quality_compare` refused the id `item_get` lists for a film's other version on Jellyfin.
- `item_delete` says when a library scan was running: a scan that had read the folder can list the item again until the next scan.
- Emby's "HDR 10" reads as HDR10.

## 0.2.0 (2026-09-23)

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
