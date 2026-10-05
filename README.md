# dalgo2d1

`dalgo2d1` is a read-only DALgo v0.90.2 adapter for Cloudflare D1. It sends typed JSON leaf queries to a trusted Worker endpoint; it never accepts, builds, or transmits SQL. The Worker owns all SQL, identifier quoting, collection/field validation, and D1 access.

## Use

Provide an explicit schema allowlist and, for database-qualified DALgo references, the matching database ID. Pin schema and seed versions when the Worker serves a fixed dataset.

```go
schema := dalgo2d1.Schema{
    "Categories": {
        Columns: []string{"CategoryID", "CategoryName", "Description", "Picture"},
        PrimaryKey: []string{"CategoryID"},
        BlobColumns: []string{"Picture"},
    },
}
db, err := dalgo2d1.New(
    "https://northwind-worker.example.workers.dev",
    schema,
    dalgo2d1.WithDatabaseID("northwind-d1"),
    dalgo2d1.WithExpectedSchemaVersion("northwind-schema-1"),
    dalgo2d1.WithExpectedSeedVersion("e74726515c3833620b54b7a50d1d273276dd23c1"),
)
```

Use `db` as a `dal.ReadSession` or `dal.QueryExecutor`. DALgo can federate local joins and aggregates by resolving each named database to a `dal.QueryExecutor`; each D1 adapter performs only its bounded single-collection reads, while DALgo evaluates the join/aggregate.

## Worker protocol

The adapter calls `GET /v1/metadata` and `POST /v1/query`. Query requests contain `version`, an allowlisted `collection`, optional direct-field `columns`, ANDed `filters`, `orders`, and `limit`/`offset`. The Worker returns typed rows and the configured primary key. BLOBs use `{"$type":"blob","base64":"..."}`; date/time values remain strings. JSON integers must be in JavaScript's safe integer range. Schema and seed pins are sent in `X-Dalgo-Schema-Version` and `X-Dalgo-Seed-Version` headers; Worker version conflicts are reported as `ErrVersionMismatch`.

The adapter pages full scans in deterministic primary-key order (all configured columns for keyless views). Its default page size is 100, matching the Worker's default query cap; custom `WithPageSize` values must not exceed the Worker's configured limit. `WithMaxRows` bounds retained rows; crossing the cap fails the query without returning partial results. Pin an immutable seed version for multi-request scans: offset paging is not a transactional snapshot, so a mutable dataset can change between pages.

## Scope

Reads, Gets and Exists are supported only for allowlisted collections with configured primary keys. Flat query scans and recordsets also support keyless views. Composite keys use the ordered fields in `PrimaryKey`. Projection, direct-field comparisons, AND filters, direct-field ordering, limits, and offsets are supported. Joins and aggregates are evaluated by DALgo federation, not sent to the Worker.

The adapter implements only DALgo's read and query interfaces, so it exposes no write or transaction methods. Arbitrary SQL, subqueries, expressions in projections, OR filters, cursor paging, and other unsupported query shapes fail closed with `dal.ErrNotSupported`. HTTPS is required except for loopback HTTP explicitly enabled for tests. Redirects are refused; bearer tokens, when configured, are sent only in the Authorization header.
