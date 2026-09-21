# pandorest

pandorest generates the Emby, Jellyfin and TMDB SDKs, `lib/emby`, `lib/jf` and
`lib/tmdb`, from their vendored OpenAPI documents.

Inspired by [Pandora](https://github.com/hashicorp/pandora), the
[go-azure-sdk](https://github.com/hashicorp/go-azure-sdk) generator: the same
importer, definitions, differ and generator pipeline, scaled down to three
documents:

```
docs/emby-openapi.json ────┐                   ┌─ api-definitions/emby/*.json ─────┐
docs/jellyfin-openapi.json ├─ import ──────────┼─ api-definitions/jellyfin/*.json ─┼─ generate ─ lib/emby, lib/jf, lib/tmdb
docs/tmdb-openapi.json ────┘  (workarounds)    └─ api-definitions/tmdb/*.json ─────┘
                                                          │
                                           diff ──────────┘  (what a refreshed spec changes)
```

```bash
make generate          # import + generate
make pandorest-diff    # what docs/*.json change against api-definitions/, breaking changes marked
make apicheck          # definitions match the specs, and every operation has a method
make gencheck          # regenerate, fail if anything is uncommitted
```

It does what the alternatives were tried and rejected for (oapi-codegen,
openapi-generator): errors for undocumented statuses rather than a result per
content type, plain string ids, readable names for Emby's machine-made
operationIds, no builder chains or nullable wrappers, and code that compiles
for both documents.

## Pieces

| Package | Pandora's | Does |
|---|---|---|
| `config` | `config/resource-manager.hcl` | the services: spec, definitions and output paths, naming, authorizer |
| `openapi` | the swagger parser | decodes the subset of OpenAPI 3 the documents use, mutably |
| `importer/workarounds` | `importer-rest-api-specs/components/dataworkarounds` | one named fix per document bug |
| `importer` | `importer-rest-api-specs` | normalises a patched document into definitions, strictly |
| `definitions` | `api-definitions/` + its models | the contract: JSON per server per tag, load, save, validate |
| `differ` | `data-api-differ` | reports operations, options, bodies, models, fields and enum values added, removed or changed |
| `generator` | `generator-go-sdk` | writes the package from definitions only |
| `lib/client` (outside) | `go-azure-sdk/sdk/client` | the hand-written base client both SDKs share |

## Importing

`import` loads each document, applies its workarounds, and normalises it:

- **Names.** Jellyfin's and TMDB's operationIds are hand-written and unique,
  so methods are named after them (`GetItems`, `GetSimilarItems`; TMDB's
  `movie-details` is `MovieDetails`). Emby's are machine-made and lossy
  (`getAudiocodecs`, and duplicates), so its methods are named after the method
  and path: `GET /Items/{Id}/Similar` is `GetItemsByIdSimilar`. Schema names
  lose their dots and underscores (`QueryResult_BaseItemDto` is
  `QueryResultBaseItemDto`); fields keep the API's spelling (`Id`, `ImdbId`),
  and a field with a leading underscore beside its plain twin takes an
  `Underscore` prefix (TMDB's `_id` beside `id` is `UnderscoreId`).
- **Types.** `Integer` (int), `Integer64`, `Float`, `Double`, `String`,
  `Boolean`, `List`, `Dictionary`, `Reference` to a model or enum, `RawObject`
  for JSON of no declared shape (untyped objects, unions, "binary" JSON
  documents), `Any`, and `RawFile` for bodies that are not JSON. An inline
  object becomes a model named after its owner and field; an operation's
  inline request and response are named after the operation, by its method
  name where methods are named by operationId (TMDB, which declares no
  shared schemas at all, answers `MovieDetails` with a
  `MovieDetailsResponse`).
- **Operations.** Path parameters in template order; query and header
  parameters as options, with lists comma-separated or one key per value as
  the document says, and a header named `Accept`, `Content-Type` or
  `Authorization` ignored, as OpenAPI says; the JSON request body, a model
  whether declared or inline, or raw bytes; the success response
  (JSON, a file, or nothing); `ExpectedStatusCodes` from the 2xx responses;
  and `Pageable` for a GET with `StartIndex` and `Limit` that answers `Items`
  and `TotalRecordCount`.
- **Grouping.** One definitions file per spec tag (Emby's `Service` suffix
  trimmed), holding the tag's operations and the models and enums only its
  operations use. Anything more than one tag uses, `BaseItemDto` above all, is
  in `Common.json`. There is one Go package per server, not per tag, because
  those shared models would otherwise tie every package to every other.

The importer is strict. An undeclared path parameter, a GET that does not say
what it answers, a duplicate operationId, an operation without a tag or a
success response, or two schemas that would declare the same Go type fail the
import with every problem listed, rather than being guessed at.

### Workarounds

A document bug is fixed with a workaround in `importer/workarounds`: a type
with a `Name` (`emby-search-term`), the `Service` it is for, the `Bug` it fixes
in a sentence, and `Apply`, which patches the loaded document. Add it to `All`.

`Apply` must first check the bug is there, and return an error when it is not:
the parameter it adds already declared, the operation gone, the schema already
carrying the field. A refreshed document that fixes the bug then fails the
import, naming the workaround to delete, instead of the workaround silently
doing nothing forever. The unit tests enforce this: every workaround must apply
to its vendored document and must fail when applied a second time.

Workarounds are for the document's shape - what an operation takes and
answers. Behaviour no document could express (Emby keying watch state by
provider id, a filter it silently drops, an add it loses mid-refresh) belongs
in `lib/embyfin`, next to the live test that found it; `docs/README.md` lists
both kinds.

## Definitions

`api-definitions/<service>/Service.json` names the package, the document's
title and version, the authorizer and the workarounds applied;
`<Group>.json` holds a tag's operations, models and constants. Operations are
sorted by name, fields by JSON name, enum values and options in document
order, so a spec refresh is a readable diff. The generator reads nothing else,
and validates what it reads (every reference resolves, no two operations
collide), so the definitions can be reviewed, and in a pinch hand-edited,
without the document.

## Diffing

`diff` compares the checked-in definitions with a fresh import of each
document, or two definitions directories with `-old` and `-new`, and prints
what changed in API terms, for example:

```
emby: 1 added, 0 removed, 2 changed (1 breaking)
  document: Emby Server API 4.1.1.0 -> Emby Server API 4.10.0.0
+ operation GetItemsFilters3 (GET /Items/Filters3)
~ operation GetItems (GET /Items)
    + option query searchterm: String
    ~ expected status codes [200] -> [200 204]
~ model BaseItemDto
    ~ field RunTimeTicks: Integer -> Integer64 [breaking]
```

A change is breaking when it would break a caller of the generated SDK: a
removal, a changed type or name, a new request body, a status code no longer
expected. `-exit-code` exits 1 when anything differs.

## Generating

`generate` writes one package per service, with a file per operation, per
model and per tag's enums:

```
client.go                                 Client, New
client_test.go                            the canned server the generated tests share
doc.go                                    package documentation, the workarounds applied
<tag>_method_<operation>.go               the method, <Name>OperationResponse,
                                          <Name>OperationOptions, and for a paged
                                          list <Name>Complete and <Name>CompleteResult
<tag>_method_<operation>_test.go          the operation's generated test
<tag>_model_<model>.go                    one model
<tag>_constants.go                        the tag's enums, with PossibleValuesFor<Enum>
```

Every generated file starts with the generated-code header; files with it that
generation no longer produces are deleted, and anything without it (the
hand-written `sdk_test.go`) is left alone.

### Generated tests

Every operation gets a test against a canned server, generated from its
definition: it calls the method with a sample value for every path parameter
(a slash in string ones, to prove escaping) and every option, and checks the
server saw the method, the escaped path, each option on the wire (lists
comma-separated or one key per value, as defined), the headers, and the body
and its content type. The server answers the first documented status with a
sample of the documented response, which must decode (or stream, for a file);
then an answer that does not decode, and a status the operation does not
document, must both come back as errors with the response. A paged list's
`Complete` must walk two pages. That exercises about 95% of each generated
package without a server.

They prove the generated code does what the definitions say, not that the
definitions are right about the server: the integration suite does that, with
a read sweep over every GET and bespoke tests on real data.

A method takes `ctx`, the path parameters, the body (`input`: the model by
value, or an `io.Reader` and a content type), then the options struct by value,
and returns `(result <Name>OperationResponse, err error)`:

```go
res, err := c.GetItems(ctx, emby.GetItemsOperationOptions{Recursive: new(true), Limit: 50})
res.HttpResponse // set whenever the server answered, its body readable again
res.Model        // *emby.QueryResultBaseItemDto

all, err := c.GetItemsComplete(ctx, emby.GetItemsOperationOptions{Recursive: new(true)})
all.Items        // every page
```

- Options are sent only when set: zero values are skipped, booleans are
  `*bool`.
- In models, booleans are `*bool` and lists and maps are `omitzero`, so a body
  can leave a flag to the server's default, send an explicit false, and clear a
  list with an empty one.
- `Model` is a pointer for a struct, enum or primitive, and the value for a
  list, map or raw JSON. An operation that answers a file has no `Model`: its
  body is left unread in `HttpResponse.Body` for the caller to read and close.
- A status outside `ExpectedStatusCodes` is a `*client.StatusError`, even a
  2xx, returned with the response. `client.IsNotFound(err)` and
  `client.WasNotFound(res.HttpResponse)` recognise a 404.
- A `Complete` pager asks for `Limit` per page (`client.DefaultPageSize` when
  unset) and stops on an empty or short page, or at `TotalRecordCount` when the
  server reports one.

## What was left out of Pandora

Resource ID types and parsers (the servers' ids are opaque strings), per-model
predicates for `CompleteMatchingPredicate`, `Default<Name>OperationOptions`
constructors (the zero value is the default), long-running operation pollers,
API versions, the Terraform and documentation generators, and the data API
server: three documents in one repository need none of it.
