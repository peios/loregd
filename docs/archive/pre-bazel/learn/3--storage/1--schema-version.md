---
title: Schema Version
description: The single-row table every hive database carries, the current version, and how a version mismatch is handled.
---

Every hive database carries a schema version in a single-row table
[*schema.every-hive-database-carries-a-schema-version]:

```sql
CREATE TABLE schema_version (
    version INTEGER NOT NULL
);
```

The current version is **1** — the only version that has existed.
[*schema.the-current-version-is-one]

loregd checks the version as it opens each database (§2.2, step 4) and
takes one of three paths:

| State | Behaviour |
|---|---|
| No `schema_version` table | The database is new. loregd creates the persistent tables, the volatile tables, and inserts version 1, all in one transaction. [*schema.a-new-database-is-created-and-stamped-in-one-transaction] |
| Version equals 1 | Normal startup. [*schema.version-one-starts-normally] |
| Version greater than 1 | Startup fails. [*schema.a-newer-version-fails-startup] The database was written by a newer loregd, and proceeding risks corrupting it by writing through an older understanding of its layout. |
| Version less than 1 | Startup fails. [*schema.a-version-below-one-fails-startup] |

**There are no migrations.** loregd carries no migration table, no
migration step list, and no upgrade path; an older database is reported
as requiring migration and startup stops there.
[*schema.there-are-no-migrations] Since 1 is the only version ever
assigned, this does not arise in practice — but a second version cannot
be stamped without building the migration machinery first.

Two details of the check are worth knowing. The `schema_version` table
has no primary key, uniqueness constraint, or check constraint, so a
second row is not detected: the first row read wins.
[*schema.a-second-version-row-is-not-detected]

And because every table is created with `IF NOT EXISTS`, a database
holding the data tables but no `schema_version` table is stamped as
version 1 without any validation that its contents match that layout.
[*schema.a-database-without-a-version-table-is-stamped-unvalidated]
