# Changelog

## Unreleased

### Breaking

- `collection_add` and `collection_remove` are `collection_edit`'s `add_items` and `remove_items`; `playlist_add` and `playlist_remove` are `playlist_edit`'s `add_items` and `remove_entries`. One call can rename, add and remove; a move still goes in a call of its own
- `library_recent` is `library_items` with `added_since`; `library_genres` is `library_filters`
- `server_logs` is gone: `server_log` lists every log file in `files` beside the newest one's tail
- `audit_missing_metadata_provider`, `audit_missing_poster` and `audit_missing_overview` are one audit, `audit_missing_metadata`: `problems` picks which to look for (all three by default), each finding names the problems its item has, `by_problem` counts each, and `audit_all` has a row a problem
- `audit_duplicate_series` is `audit_duplicates`' `folder_groups`, counted in `total_findings` and in `total_folder_groups`; `audit_all`'s duplicates row counts both
- `item_watch_history` is `server_activity` with `item`; `server_activity` also takes `user`, reads only what the server keeps (Jellyfin's retention), and says `complete` and `days` like the history tools
- `audit_runtime` reports only runtimes no film or episode can have: under 2 minutes, or 12 hours or more; it no longer compares a file with its season, and `tolerance_percent` is gone
- `runtime_multiple` and `season_median_runtime_s` are gone from `library_episodes` and `show_episodes_exist`
- `audit_provider` checks episodes too by default (`types` is Movie, Episode or both)

### Changed

- the SDKs and their generator live under `sdk/`: `sdk/emby`, `sdk/jf` and `sdk/tmdb` (generated), `sdk/client` (the base client they share), `sdk/embyfin` (the layer that makes both servers answer alike) and `sdk/pandorest` (the generator), so a Go program importing them uses the new paths

### Added

- `show_resolve` and `audit_file_path` read a file holding a run of episodes in every form the servers read and the common ones they do not (`S01E01-E02`, `S01E01E02`, `01x02-03`, `S01E01+E02`, up to twenty a file), say the file's `run_style`, and warn (`run_warning`) when the server it runs against does not read that style as a run: the file is then listed as its first episode and the rest read as missing, and the warning names the form to rename it to
- `server_activity` entries carry `item_id`, and about an item or a user the answer says how far back it could read
- `audit_missing_metadata` on a music library looks at the albums' covers alone
- `audit_provider` checks each episode's runtime against TMDB's for that episode, names the TMDB episode it compared with, and counts what it could not judge in `runtime_not_judged`
- `audit_anime_ids` says in `note` when an older copy of the anime list answered because it could not be read again
- `audit_spelling` reads album and artist names off a music library's tracks: an album spelled two ways by one album artist, an artist with and without `The`; `metadata_rename` refuses them, as they are the files' tags
- `provider_cache_clear` forgets the TMDB answers the tools keep, so the next read asks TMDB again
- `audit_previews` finds videos whose preview thumbnails - the frames over the seek bar, one BIF file a video on Emby - are missing, damaged or the wrong length, reading each file as it is on disk now; Emby's scheduled task can pass over a video it made them for before, so a file deleted since can stay missing. `item_previews_regenerate` makes them again one video at a time, by id or working through a library, and names anything else the refresh changed. Emby only so far

### Fixed

- a date from TMDB or the server that can't be read is an error naming the item, not a missing date or a guess
- a failed server read while finding a show by name, or while checking a folder, is reported instead of dropped
- a number in the anime list that can't be read is an error naming the entry, not an episode dropped
- `audit_missing_episodes` judges a show in two folders named alike as one show, rather than reporting as missing what the other folder holds
- `audit_missing_episodes` reads Jellyfin's records of episodes it has no file for (kept with the TheTVDB plugin), which its sweep never asked for
- alternative titles, translations and searches from TMDB are kept an hour, not until a restart
- `quality_compare` judges a very thinly encoded frame as thin, and gives its bits per pixel to three figures rather than as 0
- `item_delete`'s note on a scan that was running says Jellyfin can keep the deleted item listed through one more scan, not only until the next
- `quality_compare` says a film or episode the server has listed and not read yet has no frame size to compare, rather than that it "is a movie, which has no frame of its own"
- `audit_file_path`'s lookalike row no longer says on Jellyfin that a search misses the title: from 12.2 Jellyfin's search reads a lookalike letter as the Latin one (Emby's still does not)
- `library_edit` says what a rename does to an account given the library alone on Jellyfin 12.2, which moves the account to the library's new id; before 12.2 the account lost the library, and `access_lost` still names any that do

## 0.3.0 (2026-09-27)

### Breaking

- `playlist_remove` and `playlist_edit` name an entry by entry id and item (`item_ids`, `move_item_id`), need the playlist's `fingerprint` for an item held twice, and refuse when the playlist changed; removing both copies on Jellyfin needs `all_copies`
- `item_identify_apply` needs the candidate's `candidate_ids`
- `library_edit remove_paths` and `task_run` (anything but a scan) need `--enable-delete`
- `item_delete` refuses collections, playlists, libraries, genres, studios, people and artists
- `item_edit`'s `nfo_written` is `nfo_expected`
- `show_episodes` is gone (use `library_episodes`); `user_in_progress` is gone (use `user_next_up`)
- `collection_create` is marked destructive: the first collection starts a scan of every library

### Added

- `audit_whitespace`: double, stray and odd spaces in names, genres, people, folders and files
- `audit_file_path` checks a film is the film its file names, against every title it goes by and, with a TMDB token, TMDB's search and the film's collection
- every whole-library read says in `note` when the library changed while it read
- `item_get` lists every version with its own file facts and warns when one names another film
- `audit_duplicate_episodes` says how sure each group is: near certain only on proof in the files
- write tools read back what they set, say everything they change on the server and on disk, and warn when a running scan may undo it
- `audit_all` keeps every row when one audit fails and says how long each took
- `original_title` on items, task ids, `ids` for `audit_file_path` and `audit_provider`
- reads retry 502/503/504 and cut-off answers, over HTTP/1.1 and HTTP/2
- 88 tools: 61 read, 22 write, 5 delete

### Fixed

- different episodes of a steady-length show no longer read as near-certain copies
- nothing says "not there", "never watched", "missing" or a total from a read cut short; tools that decide a delete refuse instead
- `playlist_remove` no longer removes the wrong entry on Emby 4.11, which renumbers entries after a change
- `collection_create` no longer empties a renamed collection on Jellyfin; collections no longer count a series' episodes as members
- `item_set_state` no longer reports a watched mark or favourite the server dropped
- a failed delete says what already went and whether the item is still listed
- `metadata_rename` no longer answers before Emby's album genres catch up
- film and file names: part 1 and 2 kept apart, a title's own year never read as the release year, Radarr and TRaSH tags not read as titles, "can't tell" instead of a guess
- Emby versions are grouped by the key Emby merges them by, not any shared id
- `library_items` title searches give a true total; a `saved_since` that isn't a date is refused
- a parental limit of 0 shows as a limit
- a series carrying a film's ids no longer reports another show's episodes as missing
- an unknown id no longer reads as "nobody watched this" or "nothing similar"
- sort name edits on Emby are kept; `library_edit` can't rename onto another library's name
- Emby routes the SDK couldn't call now work

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
