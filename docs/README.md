# docs

## API specs

Vendored OpenAPI specs for the two supported backends — the reference for
`lib/embyfin` struct fields and endpoint shapes. These are documentation,
not build inputs; nothing is generated from them.

| File | Source | Version vendored |
|---|---|---|
| `emby-openapi.json` | <https://swagger.emby.media/openapi.json> | Emby Server API 4.1.1.0 — 356 paths, 443 operations |
| `jellyfin-openapi.json` | <https://repo.jellyfin.org/files/openapi/stable/> | Jellyfin API 10.11.11 — 315 paths, 388 operations |

To refresh, download the latest from the sources above (Jellyfin's directory
listing names files by release; Emby's URL always serves the current version).
A running server also serves its own live spec (exact for its version and
plugins): `http://<server>:8096/emby/openapi.json` on Emby.
