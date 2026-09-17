# Changelog

## Unreleased

Comparing a download folder against a big TV library: reading every episode at once, asking whether one exists, and getting a straight answer about what a series is missing.

- **`show_missing` works on Emby 4.10 again.** It used to answer `{"missing": []}` for every series, which reads as "nothing is missing". It now reads the run from TMDB when the server has no record of it (set `EMBYFIN_TMDB_KEY`), and when it still cannot tell, it says so: `supported` is false and `missing` is `null`, never an empty list.
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
