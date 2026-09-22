# api-defs

The vendored OpenAPI documents, the definitions imported from them, and what each server does that its document does not say.

## API specs

The two backends and TMDB publish OpenAPI documents, vendored at the top of
`api-defs/` as `<server>-openapi-<version>.json`, the reference for
`lib/emby`, `lib/jf` and `lib/tmdb` - and, unlike Audiobookshelf's, they are
build inputs. `internal/pandorest` (see its [README](../internal/pandorest/README.md))
imports each into checked-in definitions under `api-defs/<server>-<version>/` beside it,
fixing the document's known bugs with named workarounds on the way, and
generates the two clients from those definitions. `make generate` runs both
steps, `make pandorest-diff` reports what a refreshed document would change,
`make gencheck` (and the unit tests) fail when the generated code is stale,
and `make apicheck` proves every operation in each spec has a method.

| File | Source | Version vendored |
|---|---|---|
| `emby-openapi-4.10.0.40.json` | a stock Emby 4.10 server's own `/emby/openapi.json` | Emby Server API 4.10.0.40 - 433 paths, 499 operations (HEAD and OPTIONS skipped) |
| `jellyfin-openapi-12.1.0.json` | the running `jellyfin/jellyfin:latest`'s own `/api-docs/openapi.json` (also published at <https://repo.jellyfin.org/files/openapi/stable/>) | Jellyfin API 12.1.0 - 294 paths, 346 operations |
| `tmdb-openapi-2026.09.22.json` | <https://developer.themoviedb.org/openapi/tmdb-api.json> | tmdb-api 3 - 148 paths, 152 operations; named by the date fetched, since the document evolves under one API version |

Emby's published document (<https://swagger.emby.media/openapi.json>) is
still the 4.1.1 one, eight years behind the server, so the vendored copy is
what a running server serves about itself (`/emby/openapi.json`), and
Jellyfin's likewise (`/api-docs/openapi.json`), each from the same `:latest`
image the live tests run against. **`make spec-refresh`** does the whole
refresh: pulls the latest images, starts each server once and reads its
document, fetches TMDB's, vendors anything that changed under its version
beside the old one, runs `make generate`, prints the API-level diff between
the two versions (breaking changes marked) and removes the old pair, which git
keeps. Review the diff, run the tests, commit. A workaround whose bug the new
document fixes fails the import and names itself; delete it. Every generated
package records the document version it was built from in its `APIVersion` constant,
and `server_info` reports it beside the server's own.

The specs are documentation of intent, not of behaviour: the live suites
(`integration/`, `acceptance/`) are what prove the shapes against a real
server, and the quirks they found are recorded below.

## Where the specs are wrong

These are shape bugs, fixed in the generated clients by the importer's
workarounds (`internal/pandorest/importer/workarounds`, listed in each
`api-defs/<server>-<version>/Service.json`). Each checks its bug is still in the
document and fails the import once it is not.

- Emby (`emby-*`):
  - About 95 GETs declare a 200 with no content; a table says which answer
    JSON (and in what shape) and which answer files.
  - `POST /Users/{Id}/Images/{Type}/{Index}` has an `{Index}` placeholder and
    no parameter for it.
  - Operations with an empty response declare 200; the server answers 204.
    Operations answering JSON declare only 200, and answer 204 when the
    result is null (a timer or sync job that does not exist).
  - `UserId` (the owner) is missing from `POST /Playlists`,
    `LibraryContentType` and `IsNewLibrary` from `/Libraries/AvailableOptions`,
    and `LegacyNextUp` from `/Shows/NextUp`.
  - The two library DELETEs declare no parameters; the server needs the
    library's `Id`.
  - Array query parameters declare one key per value; the server reads one
    comma-separated value.
  - `/openapi.json` and its siblings declare a JSON string and answer the
    document; `/Environment/ParentPath` declares a JSON string and answers
    the bare path as text; `/LiveTv/Recordings/Folders` declares an array and
    answers a `QueryResult`.
  - `/Encoding/CodecInformation/Video` declares its bit rates and resolution
    rates as objects and answers display text (`"781 Mbit/s"`); an option
    editor's `PropertyCondition.Value` declares an object and answers a
    string.
- Jellyfin (`jellyfin-*`):
  - `POST /Playlists` still declares its deprecated query parameters, which the
    server answers 400 to.
  - Jellyfin 12's document leaves out the HLS streaming routes
    (`/Videos/{itemId}/master.m3u8` and the rest), which the server still
    answers. Nothing here streams, so no workaround adds them back.

- TMDB (`tmdb-*`), whose document is drawn from the examples in its docs
  site rather than from its code:
  - No operation is tagged; each is grouped by the first segment of its path.
  - Every request body is declared as one `RAW_BODY` string, the docs site's
    placeholder; the real shape is only in the request example, so it is
    taken from there (`{"value": 8.5}` for a rating).
  - A list an example left empty declares no item type, a field an example
    left null declares no type, and a field an example happened to give a
    whole number is an integer where TMDB answers fractions (`vote_average`,
    a review's `rating`). Each takes the type the same field has elsewhere in
    the document.
  - Five operations declare an id a string that every other declares an
    integer (`movie_id` on `movie-keywords`), and a list's details declare its
    `id` a string where TMDB answers a number, its item status the reverse.
  - The rating operations declare a `Content-Type` header parameter, which
    OpenAPI says to ignore; the importer ignores it in every document.

Array query parameters follow the document: Jellyfin's are sent one key per
value (its comma binder accepts both, and the parameters without it only read
repeated keys), Emby's comma-separated.

## Conventions worth knowing

- Auth is an API key, sent as `X-Emby-Token` (Emby) or in the
  `Authorization: MediaBrowser Token="..."` header (Jellyfin 12 answers 401
  to an API key sent only as `X-Emby-Token`); the clients send the form each
  server prefers. A key acts as the server itself, not as
  a user, so anything user-scoped (watch state, favourites, next up, resume)
  takes a user id.
- TMDB takes its API Read Access Token, a JWT, as an `Authorization: Bearer`
  header, and the older API Key as the `api_key` query parameter; `lib/tmdb`
  sends whichever it is given the way TMDB reads it.
- Both servers descend from the same MediaBrowser codebase, so the item
  model (`BaseItemDto`), the `/Items` query, sessions, playlists and
  collections are the same shape. The differences the neutral layer
  (`lib/embyfin`) hides:
  - Emby scopes user context by path (`/Users/{id}/Items`), Jellyfin dropped
    those routes in 10.9 and takes `userId` as a query parameter.
  - Emby 4.10 lists libraries through the paged
    `/Library/VirtualFolders/Query` (the bare `/Library/VirtualFolders` it also
    answers has wrapped the list in an envelope on some versions and not on
    others); Jellyfin returns a bare array. Creating a library: Emby takes
    everything in the JSON body, Jellyfin takes name, type, paths and refresh
    as query parameters and only `LibraryOptions` in the body. Renaming one
    and adding or removing a folder: Emby names the library by id in a body,
    Jellyfin by name in the query; Jellyfin gives a renamed library a new id
    on its next scan. A scan of one Jellyfin library does not see a folder
    added to or removed from it (only the library scan revalidates folders),
    so a folder change there asks for that scan; Emby's scan of the one
    library picks it up. The scan is asked for on its own (`POST
    /Library/Refresh`, which cancels a running scan and queues one) rather
    than with the change (`refreshLibrary=true`), because Jellyfin drops the
    latter without a word when a scan is already running, and a library made
    or changed back to back with another then never gets scanned.
  - Scanning one library is a refresh of its folder, recursive, with the
    `Default` modes that fill in only what is missing: Emby needs
    `Recursive=true`, Jellyfin refreshes a folder's children without being
    asked (and documents no such parameter). It is not the scan task, so
    `/ScheduledTasks` does not show it.
  - `AnyProviderIdEquals` (find by tmdb/imdb id) is Emby-only; on Jellyfin the
    neutral client pages the library and filters.
  - Emby ignores `IsMissing` on `/Shows/{id}/Episodes` and returns every
    episode; missing episodes are the ones with no `Path`. Neither server
    records them out of the box: stock Jellyfin needs the TheTVDB plugin,
    and Emby 4.10 no longer persists `ImportMissingEpisodes`.
  - Emby keys watch state by provider id, so marking one copy of a film
    played or favourite marks every copy with the same tmdb id; Jellyfin
    keys it by item. Emby's filtered lists (`IsPlayed`, `IsFavorite`) then
    return only some of the copies, though each copy's own read says it is
    played, so the tools count a user's watching by title (type and provider
    id): one film watched, one favourite, on both servers.
  - Emby's resume list (`/Users/{id}/Items/Resume`) includes the next
    episode of a series, at position zero and never started, once the one
    before it is marked played; the neutral client leaves such rows out.
  - Emby drops an `Ids` filter it cannot parse and answers `/Items` with the
    whole library, so an item lookup has to check the id it got back.
  - Emby reads `GenreItems` and `TagItems` on an item update and ignores the
    plain `Genres` and `Tags` lists; Jellyfin is the other way round, so an
    edit sends both.
  - Deleting a library: Jellyfin takes its name, Emby its id (a name is a
    500). Creating one: Emby applies no metadata fetchers unless the request
    lists them, so the neutral client asks `/Libraries/AvailableOptions` for
    the defaults; Jellyfin applies its own.
  - Creating a playlist: Jellyfin wants a JSON body with the owner's
    `UserId` and answers 400 to Emby's query-parameter form. Jellyfin's
    playlist read, update and move check access against the calling user,
    which an API key is not (a 400, `Guid can't be empty`), so a rename goes
    through the item update and a move takes the entries out and puts them
    back in the new order. Emby moves an entry in place.
  - Playlist entry ids: Emby numbers a playlist's entries and renumbers them
    1..n whenever it refreshes the playlist, which a library scan does, so an
    entry id read before a scan names another entry, or none, after it;
    Jellyfin's are the item ids, so a playlist holding an item twice lists
    both entries under one id and a removal of it takes both. Both answer a
    move or removal of an entry the
    playlist does not hold with a 204 and change nothing, so the neutral
    client checks the ids against the playlist first, and on Emby checks that
    a move landed, moving the entry again (found by its position) when a
    refresh renumbered the playlist in between.
  - A film that belongs to a collection vanishes from Jellyfin's `/Items`
    listing unless `CollapseBoxSetItems=false` is passed.
  - A collection is a folder named after it on both servers: creating one
    under a name that exists answers with that collection, and Jellyfin
    replaces what it holds with the new items, so `collection_create` refuses
    a name in use. Adding an item a collection already holds is answered as
    a success and changes nothing. Emby cannot create an empty collection (a
    500).
  - A user's library access (`Policy.EnabledFolders`) lists a library by its
    `Guid` on Emby (the numeric id grants nothing) and by its `ItemId` on
    Jellyfin. Jellyfin hides a library a user may not see from their views
    and single-item reads (a 404) but still lists its items when the library
    is named as the `ParentId` or an item by `Ids`, so the tools check a
    user's access before reading a library in their view. Emby keeps them out
    of its lists, but its single-item read (`/Users/{id}/Items/{id}`) answers
    for such an item and a watch state change to one is stored; the neutral
    client asks the user's list view whether they can see an item, and the
    tools refuse a change in the name of a user who cannot.
  - Neither server has a conditional update: a change is a read of the whole
    item and a post of it back, so two made at once lose one, and Jellyfin
    saves a collection's members the same way (five adds at once kept four).
    The neutral client makes its changes to one item, playlist or collection
    one at a time, which covers one embyfin-mcp process.
  - Neither server locks an edited field against a refresh, and neither needs
    to: a refresh fills only what is missing and a scan re-reads only what
    changed on disk. `ReplaceAllMetadata` replaces the edits: with the
    library's fetchers on, from the providers on both servers; with them off,
    Emby re-reads the files and nfo while Jellyfin clears the item's overview,
    genres and tags, so `item_refresh` refuses `replace_all` there.
  - An edit is written back to an nfo beside the media file only when the
    library's `Nfo` metadata saver is on (`LibraryOptions.MetadataSavers`):
    Emby then writes `<file name>.nfo` (beside a `movie.nfo` if there is one),
    Jellyfin `movie.nfo`. Both default it off for a new library, but Jellyfin
    treats a library with no saver list (`null`) as every saver on, and
    `library_create` leaves the list out unless `save_nfo` is given, so a
    library it creates on Jellyfin saves nfos and one on Emby does not.
    `library_create` and `library_edit` take `save_nfo` for either server;
    changing it posts the library's options back whole
    (`/Library/VirtualFolders/LibraryOptions` replaces them), which the
    typed models carry without losing a field on either server.
  - Applying a remote search result in a library with its fetchers off:
    Jellyfin sets the result's ids and clears the item's year and overview
    (nothing is fetched to replace them); Emby re-reads an nfo that names the
    item and keeps its ids and year, adding the result's other ids. Neither
    answer says whether the identity took (Emby answers before its refresh
    runs, Jellyfin after), so the neutral client reads the item back until it
    carries the result's id.
  - Removing a library: Emby removes its items with it. Jellyfin keeps them,
    still in playlists, collections, favourites and a user's counts and
    readable by id, until its library scan finds their folder gone, so the
    removal asks for that scan (on its own, as above).
  - The genre lists (`/Genres`) lag item edits: Jellyfin lists a new genre
    only after a scan, and both keep one no item carries any more, so
    `library_genres` reads the genres off the items.
  - A collection deleted while a metadata refresh of it is still queued
    (Emby queues one on creating it and on each change to its members, and
    the queue waits behind a library scan) is refreshed after the delete,
    which leaves that name unusable: creating a collection under it answers
    a 500 (a FOREIGN KEY constraint) until Emby restarts. Waiting, refreshing
    the collections folder, another scan and locking the collection before
    the delete do not clear or avoid it; a collection created under another
    name and renamed to it works until a refresh reads its old name back.
  - A client reporting playback (`/Sessions/Playing`, `/Progress`,
    `/Stopped`) on Emby must send the `PlaySessionId` that
    `/Items/{id}/PlaybackInfo` hands out (a 400 otherwise); Jellyfin takes the
    report without one. The activity log types the events `playback.start`
    and `playback.stop` on Emby, `VideoPlayback` and `VideoPlaybackStopped`
    (or `Audio...`) on Jellyfin. A file shorter than the servers' minimum
    resume duration is marked played when it stops.
  - Removing from a collection is applied a moment after the request is
    answered on both servers, and removing an item the collection does not
    hold is answered 204 and changes nothing, so the neutral client checks
    membership first and waits for the removal to land (sending it once more
    if it has not after a few seconds; one full Jellyfin run saw a removal
    answered and never applied, and it has not been reproduced since).
  - A library scan validates every Emby playlist and saves each as it found
    it, so a change that lands while that runs is answered, logged and saved,
    then lost. The first playlist created on a server makes Emby create its
    playlists folder, which queues such a scan by itself. The neutral client
    checks an add stayed (looking again a moment later) and sends what went
    missing once more; the integration test waits the scan out. A playlist or
    collection write that lands while a refresh rewrites one of its members
    fails with a 500 (a FOREIGN KEY constraint) rather than waiting.
  - Emby sends an item's tags only in `TagItems` (`Tags` is always null).
  - Emby's list endpoints omit `LastPlayedDate` and `PlayCount` from
    `UserData`; per-user watch state comes from the single-item read, history
    from the activity log.
  - Setting a resume point: Emby's user data update takes the position and
    the played flag from the post, a missing one as zero and false, and
    ignores the favourite; Jellyfin's applies only the fields set. A zero
    position cannot be sent at all (the generated model omits a zero number,
    and Jellyfin keeps what is omitted), so clearing one is marking the item
    unplayed, which does it on both. A position past the end of the file is
    kept, and the item is listed as in progress.
  - Filtering items by genre, tag, studio or parental rating: Emby reads each
    as one pipe-delimited value, Jellyfin as one key per value.
  - A genre, tag or studio that differs from an existing one only in case is
    folded into it on Emby; Jellyfin keeps genres case-sensitively and folds
    tags and studios. So a spelling variant worth merging differs in more
    than case, and `metadata_rename` matches the value it replaces exactly.
  - With the providers on, Jellyfin fills an item's tags from TMDB's
    keywords; Emby leaves them empty.
  - `/Items/{id}/Similar` needs a `UserId` on Emby (HTTP 500 without one).
  - `/Shows/NextUp` needs `LegacyNextUp=true` on Emby to include series a user
    has never started.
  - Jellyfin's `LibraryOptions.EnableInternetProviders` is not honoured;
    fetchers are switched off per item type with `TypeOptions` (both servers).
  - Remote search (`/Items/RemoteSearch/Movie`) with an `ItemId` uses the
    fetchers enabled for that item's library, so a library with them off
    answers `[]` on both servers; without an `ItemId` the search asks every
    provider (Jellyfin's is then answered by OMDb, with the IMDb id), and
    applying a result to the item still sets its ids, without fetching
    metadata the library does not fetch. The neutral client searches again
    without the item when a search with it finds nothing.
  - Timestamps are RFC 3339 with seven fractional digits, sometimes with no
    zone (Emby); the generated clients keep them as strings. Runtime is
    `RunTimeTicks` in 100 ns units.
- Every GET in each document is called against a real server by the read
  sweep (`integration/sweep_test.go`), and each one that does not simply
  answer is classified in `emby_sweep_test.go` / `jf_sweep_test.go`: the
  feature needs something the container lacks (a tuner, a DLNA client, a
  transcoding session, music), the route needs input its document does not
  declare (Emby's `/web/strings` and `/Users/ItemAccess` answer 500 to any
  caller), or the answer depends on a provider. A classified GET that starts
  answering fails the sweep, the way a stale workaround fails the import.
  `TotalRecordCount` is 0 on Emby's device and API key lists whatever they
  hold, which is why the generated `Complete` pagers stop on a short page as
  well as on the total.
- The generated models hold booleans as `*bool` and lists as `omitzero`
  slices, so a request body can leave a flag to the server's default (both
  servers default many to true), send an explicit false, and clear a list
  with an empty one.
- Scans, refreshes and identify run as background tasks: the request returns
  immediately and `/ScheduledTasks` says when the scan is idle.
- Both servers call the metadata providers themselves (TMDB, TheTVDB, OMDb,
  the image CDNs) and honour `HTTPS_PROXY`; on Linux they trust the
  certificates in `SSL_CERT_FILE`, which is how the tests intercept those
  calls (see `lib/providerproxy`). A call that times out makes Emby refuse
  every later call to that host (`Cancelling connection ... due to a
  previous timeout`) until it restarts, so a replay miss can fail tests that
  come after it.

