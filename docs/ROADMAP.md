# Tool roadmap

Design rules, in priority order:

1. **Wrap judgment, not plumbing.** A tool exists only where an AI has a decision to
   make. Transcoding, image delivery, session heartbeats stay unwrapped.
2. **Trim every response.** Tools return the fields a decision needs, never raw DTOs
   (`BaseItemDto` is 150+ fields; `item_get` returns ~15).
3. **Composite over chatty.** If a task always takes N calls (search candidates →
   pick → apply → verify), it is one tool, not N.
4. **Names, not just ids.** Every tool that takes a library, user, session, playlist
   or collection resolves a name, and an unknown one lists what exists.
5. **Resource-first names** (`library_*`, `item_*`, `audit_*`, `session_*`) so tools
   group by what they act on.
6. **Reads are cheap, writes are explicit, destructive is opt-in.** Every tool carries
   MCP annotations; anything that changes the server says so in its description;
   anything that deletes records or files is disabled unless the operator sets
   `--enable-delete`.
7. **Both servers, one behaviour.** A tool answers the same way on Emby and Jellyfin;
   the differences live in `lib/embyfin`, and the acceptance suite runs against both.

## Done

| Area | Tools | Answers |
|---|---|---|
| know the library | `server_info`, `library_list`, `library_get`, `library_search`, `library_items`, `library_filters`, `library_recent`, `library_genres`, `library_people`, `person_get`, `item_get`, `item_find_by_metadata_id`, `item_similar`, `show_seasons`, `show_episodes`, `show_episodes_exist`, `show_missing`, `show_resolve`, `library_episodes` | "what do I have, and what shape is it in" |
| curation | `audit_all` + 18 audits, `quality_compare`, `item_identify` → `item_identify_apply`, `item_refresh`, `item_edit`, `item_batch_edit`, `metadata_rename`, `item_artwork` → `item_artwork_set`, `item_subtitle_search` → `item_subtitle_download` | "what is wrong, and fix it" |
| watching | `user_list`, `user_get`, `user_history`, `user_next_up`, `user_in_progress`, `user_favourites`, `user_stats`, `item_last_watched`, `item_watch_history`, `item_set_watched`, `item_set_progress`, `item_set_favourite`, `item_instant_mix` | "who watched what, what is next" |
| organise | `collection_*`, `playlist_*` (create, edit, add, remove, delete) | "group these" |
| remote | `session_list`, `session_play`, `session_command`, `session_message` | "play Dune on the living-room TV" |
| admin | `server_stats`, `server_activity`, `server_devices`, `server_logs`, `server_log`, `task_list`, `task_run`, `library_scan`, `library_create`, `library_edit`, `library_delete`, `item_delete`, `audit_orphans`, `item_orphans_delete` | "keep it healthy" |

## Candidates

| Tool | Endpoints | Answers |
|---|---|---|
| `library_edit` options | `POST /Library/VirtualFolders/LibraryOptions` | switch a library's fetchers, metadata language or country (nfo saving is done as `save_nfo`, which posts the whole options object back through the typed models without losing a field on either server; these can go the same way) |
| `user_create` / `user_edit` | `/Users/New`, `/Users/{id}/Policy` | account admin |
| `intake_scan` / `intake_compare` | local ffprobe + `item_find_by_metadata_id` + `MediaSources` | "what's in this download folder, is it better than my copy" (needs filesystem access next to the files) |

## Guarded / deliberately excluded

- `item_delete` (removes the media file), `item_orphans_delete` and `library_delete`: only registered when `--enable-delete` (`EMBYFIN_ENABLE_DELETE`) is set.
- Not wrapping **as tools**, ever: streaming and transcoding, image byte
  delivery, DLNA, Sync, device pairing, server configuration and auth settings,
  user passwords, Live TV, SyncPlay, plugins and packages. `lib/emby` and
  `lib/jf` cover all of it - they are complete clients - but none of it is
  judgment an AI should be making.
