# sdk/pandorest

This is embyfin's part of [pandorest](https://github.com/katbyte/pandorest), the generator that writes `sdk/emby`, `sdk/jf` and `sdk/tmdb` from their vendored OpenAPI documents. pandorest began in this directory and is now a library of its own, shared with the other MCP servers; what stays here is what is embyfin's:

| | |
|---|---|
| `services` | the three APIs: where each document, its definitions and its package live, how its methods are named, how its lists page, which base client and authorizer it uses, and the wording of its `New` |
| `workarounds` | one named fix for each bug in the Emby, Jellyfin and TMDB documents |
| `main.go` | hands both to pandorest and runs its command |

```
api-defs/
  emby-openapi-4.10.0.40.json ──┐               ┌─ emby-4.10.0.40/*.json ──┐
  jellyfin-openapi-12.1.0.json ─┼─ import ──────┼─ jellyfin-12.1.0/*.json ─┼─ generate ─ sdk/emby, sdk/jf, sdk/tmdb
  tmdb-openapi-2026.09.22.json ─┘ (workarounds) └─ tmdb-2026.09.22/*.json ─┘
                                                        │
                                         diff ──────────┘  (what a refreshed spec changes)
```

```bash
make generate          # import + generate
make pandorest-diff    # what the specs change against their checked-in definitions, breaking changes marked
make apicheck          # definitions match the specs, and every operation has a method
make gencheck          # regenerate, fail if anything is uncommitted
```

How a document is imported, what the definitions hold, how the diff reads and what the generator writes are in [pandorest's README](https://github.com/katbyte/pandorest#readme). The generated code sends its requests through pandorest's own base client; what is each server's own about being talked to, how it takes a key and what a refusal means on it, is the `service.go` beside its generated package.

## Workarounds

A document bug is fixed with a workaround in `workarounds`, added to `All`. For the bugs most documents have, it is one of pandorest's ready-made ones, which says only what is wrong and why: `WrongAnswers` for an operation that declares one answer and gives another (`/Environment/ParentPath` declares a JSON string and answers bare text), `UndeclaredAnswers` for one that declares none, `UndeclaredParameters` for a parameter the server reads and the document leaves out. pandorest does the patching and checks the bug is still there.

Anything else is a type with a `Name` (`emby-search-term`), the `Service` it is for, the `Bug` it fixes in a sentence, and `Apply`, which patches the loaded document.

`Apply` must first check the bug is there, and return an error when it is not: the parameter it adds already declared, the operation gone, the schema already carrying the field. A refreshed document that fixes the bug then fails the import, naming the workaround to delete, instead of the workaround silently doing nothing forever. The unit tests enforce this: every workaround must apply to its vendored document and must fail when applied a second time.

Workarounds are for the document's shape, what an operation takes and answers. Behaviour no document could express (Emby keying watch state by provider id, a filter it silently drops, an add it loses mid-refresh) belongs in `sdk/embyfin`, next to the live test that found it; `api-defs/README.md` lists both kinds.

## Refreshing a document

A document and its definitions are named by the document's own version (`info.version`): `api-defs/<service>-openapi-<version>.json` imports into `api-defs/<service>-<version>/` beside it, and the highest version present is the one imported and generated from. To refresh a spec, vendor the new document beside the old one; `make generate` then imports it into its own definitions directory, `make pandorest-diff` says what changed, and the old pair is deleted once the new one is in. `scripts/spec-refresh.sh` does this from running servers.
