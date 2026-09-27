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
- `audit_whitespace`: doubled, leading, trailing and odd spaces, and a space before a colon or a file extension, in names, sort names, original titles, genres, tags, studios, people, folders and file names, with the fixed text beside each.
- Every whole-library read says in `note` when the library changed while it read it (every audit, `library_export`, `library_items` title searches, `user_stats`, `metadata_rename`, `item_orphans_delete`); `audit_all` names the audits that saw it. It reads every match's id once more at the end to check.

**Changed**

- `show_episodes` is gone: `library_episodes` takes the show by name or id (`series`) and a `season`, so one tool reads a show's episodes, a season's or a library's.
- `user_in_progress` is gone: `user_next_up` answers what to watch next and everything part way through (`in_progress`, was `resume`), with where each resumes.
- `--allow-tools` without `--toolsets` chooses from every tool, so `essential` loads all five. Beside `--toolsets`, naming a tool the sets don't hold is refused, naming the set to add.
- `item_refresh` waits for the refresh to land and says whether it did, so an edit made straight after is no longer undone.
- `item_identify_apply` sets every id the chosen title goes by when the library's fetchers are off. It warns when an nfo beside the file may bring the old title back and, on Emby, that watch state follows the ids.
- `item_delete`'s preview lists everything the server will take, including another item's nfo, subtitles and images whose names start with the same name.
- `item_artwork_set` says when Emby deletes the poster file beside the media.
- `audit_unwatched` and `item_last_watched` count watches by accounts that have since lost access to the library.
- `collection_delete` on Jellyfin watches a just-changed collection long enough for a slow refresh to come back, and says why.
- `audit_provider` pages in its own order, so every film is asked about once; the other paged lists break ties by date added.
- Breaking: `playlist_remove` and `playlist_edit` name an entry by its entry id and the item it holds (`item_ids`, `move_item_id`), need the playlist's `fingerprint` for an item held twice, and refuse when the playlist changed. Removing both copies of a doubled item on Jellyfin needs `all_copies`.
- Breaking: `item_identify_apply` takes the candidate's `candidate_ids` and applies only the candidate carrying them; a candidate without ids can't be applied.
- Breaking: `library_edit remove_paths`, and `task_run` for anything but a scan, need `--enable-delete`.
- Breaking: `item_delete` refuses collections, playlists, libraries, genres, studios, people and artists, naming the right tool.
- Breaking: `item_edit`'s `nfo_written` is `nfo_expected`, for films, shows, seasons and episodes only.
- `item_set_state` lists every item's state before a change, reads back what it set and is an error for anything the server didn't keep, counts what a folder stores as well as the rows shown, says what a mark in a limited user's name reaches, names copies elsewhere that changed with it, and refuses a folder mark reaching more than 1,000 items.
- Write and delete tools say everything they change, on the server and on disk; MCP hints match (`collection_create` is destructive: the first collection starts a scan of every library).
- Write tools say when a library scan was running that may undo or redo the change: `item_set_state`, `collection_create`, `collection_add`, `collection_remove`.
- Collections and playlists are read whole, whoever can see their contents; on Jellyfin with no administrator seeing every library the read is refused and nothing is deleted.
- `audit_duplicate_episodes` says how sure each group is (near certain only on file proof, lead, far apart) and what the files show; placeholder titles like "TBA" never count.
- Version, duplicate and file-path warnings check a film whose file name has more after its year ("Dune (2021) Part Two", "Alien (1979) - Aliens") against TMDB and the film's TMDB collection, at most 500 new films per call ("call again to continue"). "Probably" only when a year or TMDB says another film; "may" otherwise.
- `audit_all` keeps every row when one audit fails, says how long each took, and names libraries no audit reads.
- When a read can't be sure, read tools answer with what they read and say so; nothing claims "not there", "never watched", "missing" or a total from a read cut short. `metadata_rename`, `item_orphans_delete` and `playlist_add` refuse instead.
- On Emby, audits of what people are shown place each version the admin view folds away by the key Emby merges by, checking 20 against a read of each.
- `plan_check` treats an empty folder listing up the path as not known, and says when the server can't see a folder its library holds.
- Reads retry a 502, 503 or 504 and a cut-off answer, over HTTP/1.1 and HTTP/2; writes are sent once.
- One TMDB breaker for every TMDB check; a cancelled call doesn't count.
- 88 tools: 61 read, 22 write, 5 delete.

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
- `playlist_remove` on Emby 4.11 could remove the wrong entry: Emby renumbers entries a moment after a change.
- `collection_create` could empty a renamed collection on Jellyfin, whose folder names turn `/ \ : * ? " < > |` into spaces.
- Jellyfin collections were read recursively, counting a series' seasons and episodes as members.
- `metadata_rename` answered before Emby's album genres caught up; it waits up to ten seconds and names anything still showing the old name.
- `item_refresh` and `item_identify_apply` name files removed from or added beside the media, and say a file overwritten in place (the nfo) isn't seen.
- A failed delete says what already went from disk and whether the item is still listed.
- `audit_file_path` title and year rules: part 1 and part 2 kept apart, "Part One"/"Part 1"/"Pt. I" read alike, a closing "One" of a name ("Air Force One") isn't a part, country qualifiers ("(US)") match, "(OVA)"/"(TV)"/"(DC)" are words, Radarr and TRaSH release tags are not title words, a title's own year ("Blade Runner 2049") is never the release year, and a year after next year never dates a file.
- `library_items` title searches gave a false total (Emby 0, Jellyfin at most three pages) and Jellyfin listed nothing past three pages.
- A `saved_since` that isn't a date is refused.
- User tools show a parental limit of 0 (Jellyfin's strictest) as a limit.
- `audit_unwatched` listed items part way through as never watched.
- `audit_spelling` grouped different studios whose names contain each other ("Warner Bros. Pictures" and "Warner Bros. Television").
- `audit_disc_folders` counted a disc's titles more than once.
- The history tools found nothing for a series or season id, and on Jellyfin called a window complete past the 30 days its activity log keeps.
- `audit_runtime` judged a season split between two lengths by one median; each length is judged on its own.
- TMDB collection parts decode their titles (a spec workaround, `tmdb-collection-parts`).

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
