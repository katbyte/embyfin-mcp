# Changelog

## 0.5.0 (2026-10-10)

### Breaking

- `--allow-tools` beside `--toolsets` adds the tools it names to the sets: `--toolsets curation --allow-tools task_list` is curation and `task_list` as well. It used to keep only what both held, and refused a name the sets did not hold. On its own it is still only the tools it names. Narrow a set with `--deny-tools`

### Added

- `server_health` says how the server is doing in one call: tasks running and failed, who is playing and how, and what the last stretch of the log shows
- `server_log_search` searches the log between two times or over its last stretch, as entries, counts by message, a histogram, the gaps of a freeze, or the slow answers and requests still waiting
- `task_get`, `task_stop` and `task_edit`; `task_list` gives progress, what starts each task and how its last run went
- `session_list` says how each device plays, as the file is or transcoded and why; `details` adds the file and the whole transcode
- `server_config` reads the server's settings in groups; `server_config_edit` changes the ones that tune Jellyfin's trickplay images and scanning
- `library_options` puts every library's settings side by side, and says which libraries are set differently
- `server_plugins` lists what is installed into the server
- `server_info` says whether a restart is pending or an update is out, and what the log says of the machine
- `--config` (or `EMBYFIN_CONFIG`) names the settings file, so two instances can serve two servers side by side
- 89 tools: 62 read, 22 write, 5 delete

### Changed

- built on go-kt 0.5.1: `audit_whitespace` sees a space before the extension when spaces trail the name too, and counts a no-break space beside an ordinary one as a double space
- test servers and builds take their images from Google's mirror of Docker Hub first

### Fixed

- `audit_spelling` reported an artist's numbered albums, "II" beside "III", as one name typed two ways
- `audit_whitespace` gave an empty suggestion for a name that is nothing but spaces, and said to rename to it

## 0.4.1 (2026-10-09)

### Fixed

- the release build takes its images from Google's mirror of Docker Hub. Docker Hub turned the 0.4.0 build away every time it was tried, so 0.4.0 was tagged and never published: this release is what 0.4.0 was to be

## 0.4.0 (2026-10-09)

### Breaking

- `collection_add` and `collection_remove` are `collection_edit`'s `add_items` and `remove_items`; `playlist_add` and `playlist_remove` are `playlist_edit`'s `add_items` and `remove_entries`
- `library_recent` is `library_items` with `added_since`; `library_genres` is `library_filters`
- `server_logs` is gone: `server_log` lists every log file beside the newest one's tail
- the three missing-metadata audits are one, `audit_missing_metadata`, with `problems` to pick from
- `audit_duplicate_series` is `audit_duplicates`' `folder_groups`
- `item_watch_history` is `server_activity` with `item`
- `audit_runtime` reports only runtimes no film or episode can have: under 2 minutes, or 12 hours or more. `tolerance_percent`, `runtime_multiple` and `season_median_runtime_s` are gone
- `audit_provider` checks episodes too by default
- the SDKs and their generator moved under `sdk/`, so a Go program importing them uses the new paths

### Added

- `audit_previews` finds missing, damaged or wrong-length preview thumbnails, and `item_previews_regenerate` makes them again (Emby only so far)
- `audit_spelling` checks album and artist names
- a file holding a run of episodes is read in every common form, and `run_warning` says when the server will not read it as a run
- `audit_provider` checks each episode's runtime against TMDB's
- `provider_cache_clear` forgets the TMDB answers the tools keep
- `server_activity` takes `item` and `user`, carries `item_id`, and says how far back it could read
- `audit_missing_metadata` on a music library looks at the albums' covers alone
- `audit_anime_ids` says when an older copy of the anime list answered
- 80 tools: 56 read, 19 write, 5 delete

### Changed

- serving, choosing which tools a session gets, the locks that keep two edits of one item apart, the spelling and whitespace checks, and the tests' recording proxy are go-kt's, shared with the other MCP servers
- over HTTP, a session its client left open and stopped using is closed after half an hour

### Fixed

- stopping the HTTP server with a client still connected no longer takes ten seconds and exits with an error
- a date or a number from TMDB, the server or the anime list that can't be read is an error, not a guess
- a failed server read while finding a show or checking a folder is reported instead of dropped
- `audit_missing_episodes` judges a show in two folders named alike as one show, and reads Jellyfin's records of episodes it has no file for
- TMDB titles and searches are kept an hour, not until a restart
- `quality_compare` judges a thinly encoded frame as thin, and says when the server has not read a file yet
- `item_delete` says Jellyfin can keep an item deleted during a scan listed through several more scans
- `audit_file_path` no longer misreads a dotted acronym with a number after it (`Q.R.S.1`), nor says a Jellyfin search misses a lookalike letter
- `audit_runtime` leaves a disc image (`.iso`) unjudged
- `library_edit` says what a rename does to an account given that library alone on Jellyfin 12.2

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
