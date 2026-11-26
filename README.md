# strata

A database written from scratch in Go, one layer at a time, to understand what happens under Postgres or SQLite. No dependencies: only the standard library.

This is a learning project. Every step produces a working, tested database.

## Getting started

```bash
go run ./cmd/strata            # opens or creates ./strata.log
go run ./cmd/strata other.log  # or any other file
```

```
strata> SET name Issa
OK
strata> GET name
Issa
strata> DEL name
1
strata> GET name
(nil)
strata> COMPACT
OK
```

Data survives restarts. To run the tests: `go test ./...`

## Target architecture

```
client (REPL, then TCP)
   │
SQL parser       text → AST
Executor         AST → operations
Tables           row → encoded key/value
B-tree           sorted keys, lookups and ranges
Pager            4 KB pages, cache
WAL              log, crash recovery
   │
file on disk
```

## Roadmap

- [x] **1. In-memory KV**: `SET`, `GET`, `DEL` and the REPL
- [x] **2. Append-only log**: binary format with CRC32, `fsync`, replay on open, truncation of a tail damaged by a crash
- [x] **3. Compaction**: rewrite the log with the latest value of each key, on demand (`COMPACT`) or automatically
- [ ] **4. Pager and B-tree**: page-based storage, range queries
  - [x] 4a. Node format: fixed-size pages, offset table for binary search, copy-on-write builders
  - [x] 4b. Insert and lookup: splits that propagate up, the tree grows from the root
  - [x] 4c. Delete: merging small nodes, the tree loses a level when the root has one child
  - [x] 4d. Pager on disk: copy-on-write commits, two alternating meta pages, crash recovery
  - [ ] 4e. Free list: reuse pages freed by earlier commits
  - [ ] 4f. Range scans, and the B+tree replaces the log behind the REPL
- [ ] **5. Transactions**: `BEGIN`, `COMMIT`, `ROLLBACK`, crash recovery
- [ ] **6. Mini-SQL**: `CREATE TABLE`, `INSERT`, `SELECT … WHERE`
- [ ] **7. TCP server**: multiple concurrent clients

Then the same path in Rust.

## Log format

Each operation is appended to the end of the file (little-endian integers):

```
| crc32 (4) | op (1) | key len (4) | value len (4) | key | value |
```

`op` is 1 for `SET` and 2 for `DEL`. The CRC32 covers everything after it. On startup, the log is replayed in order; an incomplete record, or one with a bad CRC, marks the end of the valid log, and the file is truncated there.

## Compaction

The log keeps every version of a key. Compaction writes a fresh log (`<file>.compact`) with a single `SET` per live key, syncs it, then renames it over the old one. Since `rename` is atomic, a crash leaves either the old log or the new one, never a mix. It triggers automatically once the log holds 1000 records and more than half of them are stale.

## B+tree file format

The file is an array of 4 KB pages; page `n` starts at byte `n × 4096`. Pages 0 and 1 are meta pages, every other page holds one tree node.

```
meta page: | magic "STRATA01" (8) | version (8) | root page (8) | page count (8) | crc32 (4) |
node page: | type (2) | nkeys (2) | child pointers (nkeys × 8) | offsets (nkeys × 2) | key/value pairs |
```

Pages are never modified in place. A write copies the path from the leaf to the root into new pages, `fsync`s them, then writes a new meta page pointing to the new root and `fsync`s again. The two meta slots alternate (version `v` goes to slot `v % 2`), so a meta write cut short by a crash only damages the slot that was not in use. On open, the valid meta with the highest version wins.

## Layout

```
kv/          the storage engine (Store, record format)
btree/       B+tree nodes, the tree, and the pager that stores it in a file
repl/        one command line → one response
cmd/strata/  the interactive program
```
