# Tool roadmap

Design rules, in priority order:

1. **Wrap judgment, not plumbing.** A tool exists only where an AI has a decision to
   make. Transcoding, image delivery, session heartbeats stay unwrapped.
2. **Trim every response.** Tools return the fields a decision needs, never raw DTOs
   (`BaseItemDto` is 150+ fields; `item_get` returns ~12).
3. **Composite over chatty.** If a task always takes N calls (search candidates →
   pick → apply → verify), it is one tool, not N.
4. **Resource-first names** (`library_*`, `item_*`, `session_*`) so tools group by
   what they act on.
5. **Reads are cheap, writes are explicit, destructive is opt-in.** Anything that
   changes the server says so in its description; anything irreversible is disabled
   unless the operator sets a flag.

## Phase 1 — know the library (done)

| Tool | Endpoints | Answers |
|---|---|---|
| `server_info` | `/System/Info` | "is it up, what version" |
| `library_list` | `/Library/VirtualFolders` | "what libraries exist" |
| `library_get` | `/Library/VirtualFolders` + `/Items` counts | "how big is Movies" |
| `library_search` | `/Items?SearchTerm` | "do I have Dune" |
| `item_get` | `/Items?Ids` | "what quality is my copy" |
| `item_lookup_provider` | `/Items?AnyProviderIdEquals` | "do I have tmdb 89998" |

## Phase 2 — curation core (the reason this server exists)

| Tool | Endpoints | Answers |
|---|---|---|
| `library_audit` | paged `/Items` + detectors (+ TMDB API) | "what's mismatched or unmatched" |
| `item_identify` | `/Items/RemoteSearch/{Movie,Series}` + `/Items/RemoteSearch/Apply/{Id}` | "fix this wrong match" (composite: search → apply → verify) |
| `item_refresh` | `/Items/{Id}/Refresh` | "re-pull metadata for this" |
| `library_scan` | `/Library/Refresh` | "pick up the files I just added" |
| `library_recent` | `/Items?SortBy=DateCreated` | "what got added this week" |
| `library_stats` | `/Items/Counts` | "how much stuff do I have" |
| `library_duplicates` | audit detector (same provider id, 2+ items) | "what do I have two copies of" |

Supporting non-tool work: `lib/audit` detector functions, `lib/tmdb` client,
state file for verified/unmatchable checkpoints, `audit` CLI subcommand.

## Phase 3 — watch state and playback

User-context tools take an optional `user` (name or id, via `/Users`); sessions are
live devices.

| Tool | Endpoints | Answers |
|---|---|---|
| `user_list` | `/Users` | "who has accounts" |
| `item_set_watched` | `POST/DELETE /Users/{UserId}/PlayedItems/{Id}` | "mark season 2 watched" |
| `item_set_favourite` | `POST/DELETE /Users/{UserId}/FavoriteItems/{Id}` | "favourite this" |
| `user_next_up` | `/Shows/NextUp`, `/Users/{UserId}/Items/Resume` | "what should I continue" |
| `session_list` | `/Sessions` | "who's watching what right now" |
| `session_play` | `POST /Sessions/{Id}/Playing` | "play Dune on the living-room TV" |
| `session_command` | `/Sessions/{Id}/Playing/{Command}` | "pause the bedroom TV" |

## Phase 4 — organise and polish

| Tool | Endpoints | Answers |
|---|---|---|
| `playlist_create` / `playlist_edit` | `POST /Playlists`, `/Playlists/{Id}/Items` | "make a Halloween playlist" |
| `collection_create` / `collection_edit` | `POST /Collections`, `/Collections/{Id}/Items` | "group the Bond films" |
| `item_similar` | `/Items/{Id}/Similar` | "what's like this" |
| `item_subtitle_search` / `item_subtitle_download` | `/Items/{Id}/RemoteSearch/Subtitles/{Language}` | "get English subs for this" |
| `task_list` / `task_run` | `/ScheduledTasks`, `POST /ScheduledTasks/Running/{Id}` | "run the library scan task" |
| `activity_log` | `/System/ActivityLog/Entries` | "what happened on the server" |

## Phase 5 — intake (needs filesystem access next to the files)

| Tool | Mechanism | Answers |
|---|---|---|
| `intake_scan` | local ffprobe + parse + `item_lookup_provider` | "what's in this download folder" |
| `intake_compare` | probe vs library `MediaSources` + quality ranking | "is this better than my copy" |

## Guarded / deliberately excluded

- `item_delete` (`DELETE /Items/{Id}`): only registered when `--enable-delete`
  (`EMBYFIN_ENABLE_DELETE`) is set; description warns it removes the file.
- Not wrapping, ever: streaming/transcode endpoints, image byte delivery, DLNA,
  Sync, device pairing, server configuration mutation, user creation/passwords,
  Live TV (revisit only if a real use case shows up).

Rough coverage math: ~28 tools over ~35 of Emby's 443 operations. The other ~408
are plumbing for Emby's own client apps, not decisions.
