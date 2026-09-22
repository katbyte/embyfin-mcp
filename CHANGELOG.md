# Changelog

## Unreleased

- **Season 0 can be asked for.** Every number in a generated SDK option is now a pointer, so `show_episodes` and `library_episodes` take `season: 0` for the specials and an image index of 0 means the first image; before, a 0 was "not asked" and the whole series came back.
- `library_items` and `library_episodes` with `saved_since` on Emby no longer drop the filter when a user, `watched` or `sort=played` is also given.
- `audit_quality` judges a widescreen encode by its width (1280x536 is 720p), `audit_duplicates` keeps every copy of a film matched on two ids in one group, `audit_year_mismatch` reads the film's own year rather than a collection folder's, and `audit_missing_episodes` counts both halves of a double-episode file.
- A `TMDB_KEY`/`--tmdb-key` set on the command line or in the environment now beats a `TMDB_TOKEN` in the config file, as documented.
- Moving a Jellyfin playlist entry takes out and puts back only the entries from the lower of the two positions on, checks they landed, and sends them again if a scan's re-read of the playlist file undid it.
- A Jellyfin library made, deleted or given a folder asks for its library scan on its own (`POST /Library/Refresh`), which is never dropped, rather than with the change, which Jellyfin drops silently when a scan is already running.
- The provider proxy stores gzipped answers decoded, scrubs a redacted value from headers as well as the body, elides plugin binaries, and no longer records the server's own public address.

Comparing a download folder against a big TV library: reading every episode at once, asking whether one exists, and getting a straight answer about what a series is missing.

- **`show_missing` works on Emby 4.10 again.** It used to answer `{"missing": []}` for every series, which reads as "nothing is missing". It now reads the run from TMDB when the server has no record of it (set `EMBYFIN_TMDB_TOKEN`), and when it still cannot tell, it says so: `supported` is false and `missing` is `null`, never an empty list.
- **`library_episodes`**: every episode in a library in one paged call, with resolution, codec, bitrate, size and runtime on each row. Previously this meant one `show_episodes` call per series.
- **`show_episodes_exist`**: ask whether a series has particular episodes without listing the whole series. A file holding two episodes counts for both.
- **`show_resolve`**: turn a release name into a library series, scored, so a client can refuse a weak match instead of guessing. Handles scene names and FileBot names.
- `show_episodes` rows now carry the same quality numbers, and take a season number as well as a season id.
- `audit_missing_episodes` gained `runs_known`, for the same reason: when the server has no record of a series' run, a series it does not list is not a series proved complete.
- **`quality_compare`**: which of two copies of an episode or film is better, by how much, and why, with every number and constant it used. It compares; it does not say what to do.
- `show_episodes_exist` takes up to 50 series in one call, can add quality facts to the episodes it finds, and says how confidently it matched a name and which other library entries hold the same show.
- Quality rows carry frame rate and HDR, which is what gives away an upscaled or frame-interpolated copy.
- `fields` on `library_episodes` and `show_episodes_exist` returns only the facts asked for.
- A series name no longer resolves to the only candidate when that candidate is a poor match, and a spin-off no longer resolves to its parent (`Law and Order SVU` to `Law & Order`).
- Release names with a site prefix, dotted acronyms (`Chicago P.D.`), accents, or a word like `Max` or `Stan` at the start of the title now resolve.
- **`plan_check`**: before writing files into the library, what is at each destination path now, which series each path would join, and which entries collide with each other. Reads only. A file written over an existing path keeps the item's id and date, so nothing afterwards can show it happened.
- **`audit_duplicate_titles`**: one episode's content filed under two episode numbers, which neither other duplicate audit can see.
- **`audit_title_mismatch`**: episodes whose file name claims a different title from the one the server holds.
- **`audit_duplicate_series_folders`**: shows held twice because two folder names differ only by spacing, case, an accent or punctuation.
- **`audit_orphans`**: items the server still holds under a folder no library covers, which is what a renamed or removed library folder leaves behind. No library lists them, but every sweep of the server counts them.
- **`audit_disc_folders`**: a disc copied into the library as its own files rather than as a film. With no BDMV or VIDEO_TS structure around them the server makes a film of each stream and matches each on its own, which files short clips under the names of other films.
- **`audit_anime_ids`**: anime held against Anime-Lists, the community mapping of every AniDB entry to where TVDB and TMDB hold it. It finds series whose ids disagree, specials that are an OVA or a film with an AniDB entry of their own and could be split out into a series, and series already kept apart, with the entry that justifies it. The list is fetched from GitHub once a day, or read from `--anime-list`.
- **`lib/tmdb` is generated**, like `lib/emby` and `lib/jf`, from TMDB's own OpenAPI document: all 152 operations, typed, each with a generated test, and a sweep that calls every read against the real API, recorded once with a token and replayed with none (`make test-tmdb`). TMDB's document is drawn from the examples in its docs, and seven workarounds fix what that gets wrong. The audits and `show_missing` ask TMDB through it.
- **`audit_movie_ids`**: films whose ids disagree, asked of TMDB: a TMDB id whose film carries a different IMDb id, a TMDB id TMDB no longer has, or an IMDb id that is a series or an episode. Needs `EMBYFIN_TMDB_TOKEN`.
- The TMDB setting is `--tmdb-token` / `EMBYFIN_TMDB_TOKEN`, after TMDB's own name for the credential. `--tmdb-key` / `EMBYFIN_TMDB_KEY` still work.
- A `.embyfin-mcp` file's two-word settings were ignored: `TMDB_TOKEN` there now sets `--tmdb-token`, and likewise `READ_ONLY`, `ENABLE_DELETE` and the rest.
- `audit_missing_metadata_provider` counts an id from any provider as a match, though not a link to the item's website or social pages, so an anime matched only on AniDB or MyAnimeList is no longer reported. `missing` drills down to the providers named (tmdb, imdb, tvdb, anidb, myanimelist): `missing=tmdb` finds items matched elsewhere but not on TMDB, and each finding lists the ids the item does have. `ignore` leaves out libraries whose items never carry an id, such as a YouTube library.
- **`item_orphans_delete`**: deletes those items, only under a folder the server can no longer see, because deleting an item deletes its file. It shows what it would delete unless `confirm` is set, and needs `--enable-delete`.
- Episode rows carry `date_created` and `file_modified` (Emby only), and `library_episodes`/`library_items` take `saved_since`. An overwritten file keeps its item's creation date, so this is the only way to see one.
- Episode rows carry `runtime_multiple` and `season_median_runtime_s` where the call read a whole season: about 2 means the file holds two episodes under one number, which is why the number beside it looks missing.
- `hdr` is now present on every row with video and says `sdr`, `hdr10`, `hlg`, `dovi`, `dovi_hdr10` or `unknown`. It used to be absent for three different reasons and callers could not tell them apart.
- `audit_runtime` expects a file the server records as holding several episodes to run that many times the season median, and reports an impossible duration as broken metadata rather than as a percentage.
- `audit_duplicates` groups episodes by provider id AND season and episode number, because one shared id across unrelated episodes is common; `audit_all` now includes episodes in that pass rather than only films and series.
- **`audit_language`**: find films and episodes by the language of their audio or subtitles, including what cannot be watched in a language at all. A track with no language tag is counted separately, never as lacking the language.
- `server_info` reports `embyfin_mcp_version`, the build answering, so a session can tell when it is running an old binary.
- Series names are matched against the library's full list of series, read once and kept, rather than the server's search. A batch of names costs one read, shows the search failed to return now resolve, and finding a show held under two entries no longer costs a search per series.
- Tool descriptions and input schemas are shorter, so the `curation` toolset costs about a thousand fewer tokens of context.
- **Breaking:** every size is in bytes and every duration or position in seconds. `item_get` and the tools that share its item rows return `size`, `runtime_s` and the same video fields as episode rows (`width`, `height`, `video_codec`, `bitrate`, `frame_rate`, `hdr`) instead of `size_mb`, `runtime_minutes` and a `video` string. `position_minutes`, `resume_minutes` and `seek_minutes` are `position_s`, `resume_s` and `seek_s`, `server_logs` returns `size`, and `audit_quality` takes `min_bitrate` in bits per second.
- **Breaking:** audio tracks are objects with language, codec, channels and bitrate instead of strings like `eng eac3 6ch`, everywhere a tool reports them; `server_info`'s `version` is now `server_version`; and `library_episodes` with `quality: false` no longer includes the path.

## 0.1.1 (2026-09-15)

No code changes. 0.1.0 published its binaries and image but not the Homebrew
formula, because the tap token was not set; this release runs that path with
the token in place.

## 0.1.0 (2026-09-15)

Initial release.

- **81 tools** for Emby and Jellyfin, in toolsets: 12 audits, identify, artwork, subtitles,
  metadata edits, watch state, playlists, collections, sessions and server admin. `core` (7
  read-only tools) is the default; `--toolsets`, `--allow-tools`/`--deny-tools`, `--read-only`
  and `--enable-delete` decide the rest.
- **CLI**: `serve`, `info`, `tools`, `version`. Flags, `EMBYFIN_*` environment variables or a
  `.embyfin-mcp` file.
- **Transports**: stdio, or Streamable HTTP with `--listen` (bearer token required). Alpine
  Docker image at `ghcr.io/katbyte/embyfin-mcp`, plus `docker-compose.yml`.
- **Two generated Go SDKs**, `lib/emby` (Emby 4.10, 499 operations) and `lib/jf` (Jellyfin
  12.0, 346), written by `internal/pandorest` from the servers' own OpenAPI documents, over a
  shared base client. `lib/embyfin` is the thin layer that makes both servers answer alike.
- **Tested against real servers**: unit tests, an SDK suite and a tool suite against Emby and
  Jellyfin in Docker, with provider calls recorded and replayed. Every registered tool must
  have a test, every GET is swept, and `make cover` merges the lot into the coverage badge.
