# strata

A database written from scratch in Go, one layer at a time, to understand what happens under Postgres or SQLite. No dependencies: only the standard library.

This is a learning project. Every step produces a working, tested database.

## Getting started

```bash
go run ./cmd/strata            # opens or creates ./strata.db
go run ./cmd/strata other.db   # or any other file
```

```
strata> SET pomme 3
OK
strata> SET ananas 1
OK
strata> SET cerise 7
OK
strata> GET pomme
3
strata> SCAN
ananas = 1
cerise = 7
pomme = 3
strata> SCAN b d
cerise = 7
strata> DEL pomme
1
strata> BEGIN
OK
strata*> SET ananas 0
OK
strata*> ROLLBACK
OK
```

SQL runs on top of the same storage:

```
strata> CREATE TABLE eleves (id INT, nom TEXT, ville TEXT, note INT)
CREATE TABLE
strata> INSERT INTO eleves VALUES (1, 'Issa', 'Abidjan', 15), (2, 'Awa', 'Bouaké', 18), (3, 'Koffi', 'Abidjan', 12)
INSERT 3
strata> SELECT nom, note FROM eleves WHERE id >= 2 AND id <= 3
nom   | note
------+-----
Awa   |   18
Koffi |   12
(2 rows, primary key range scan)
```

`SCAN [start [end]]` lists the keys in `[start, end)` in order. Data survives restarts and crashes. To run the tests: `go test ./...`

## Target architecture

```
client (REPL, then TCP)
   │
SQL parser       text → AST
Executor         AST → operations
Tables           row → encoded key/value
B+tree           sorted keys, lookups and ranges
Pager            4 KB pages, copy-on-write commits, free list
   │
file on disk
```

## Roadmap

- [x] **1. In-memory KV**: `SET`, `GET`, `DEL` and the REPL
- [x] **2. Append-only log**: binary format with CRC32, `fsync`, replay on open, truncation of a tail damaged by a crash
- [x] **3. Compaction**: rewrite the log with the latest value of each key
- [x] **4. Pager and B+tree**: page-based storage, range queries
  - [x] 4a. Node format: fixed-size pages, offset table for binary search, copy-on-write builders
  - [x] 4b. Insert and lookup: splits that propagate up, the tree grows from the root
  - [x] 4c. Delete: merging small nodes, the tree loses a level when the root has one child
  - [x] 4d. Pager on disk: copy-on-write commits, two alternating meta pages, crash recovery
  - [x] 4e. Free list: reuse pages freed by earlier commits
  - [x] 4f. Range scans (`SCAN`), and the B+tree replaces the log behind the REPL
- [x] **5. Transactions**: `BEGIN`, `COMMIT`, `ROLLBACK`, several writes in a single commit, readers never blocked by the writer
- [x] **6. Mini-SQL**: `CREATE TABLE`, `INSERT`, `SELECT … WHERE … LIMIT`, `UPDATE`, `DELETE`, range scans on the primary key
- [ ] **7. TCP server**: multiple concurrent clients

Then the same path in Rust.

Steps 2 and 3 were replaced by the B+tree in step 4. The append-only log and its compaction are kept under the git tag [`step-3-append-only-log`](https://github.com/diallosouleymane/strata/tree/step-3-append-only-log).

## File format

The file is an array of 4 KB pages; page `n` starts at byte `n × 4096`. Pages 0 and 1 are meta pages, every other page holds a tree node or a piece of the free list. Integers are little-endian.

```
meta page:      | magic "STRATA02" (8) | version (8) | root page (8) | page count (8) | free list head (8) | crc32 (4) |
node page:      | type (2) | nkeys (2) | child pointers (nkeys × 8) | offsets (nkeys × 2) | key/value pairs |
free list page: | type 3 (2) | count (2) | next page (8) | free page ids (count × 8) |
```

In a node, each pair is `| key len (2) | value len (2) | key | value |`, and the offset table gives direct access to the i-th pair, so a node can be binary searched. Internal nodes hold the first key of each child; leaves hold the keys and values. Keys are at most 1000 bytes and values at most 3000, so any single pair fits in a page.

## Crash safety

Pages are never modified in place. A write copies the path from the leaf to the root into new pages, `fsync`s them, then writes a new meta page pointing to the new root and `fsync`s again. Until that second `fsync`, the previous tree is complete and untouched, so a crash at any point leaves either the old version or the new one.

The two meta slots alternate (version `v` goes to slot `v % 2`), so a meta write cut short by a crash only damages the slot that was not in use. On open, the valid meta with the highest version wins.

Freed pages are listed in a chain of free list pages that the meta points to. A page freed by commit `N` still belongs to the tree of version `N - 1`, the one a crash would fall back to, so it only becomes reusable from commit `N + 1`.

## SQL

The SQL layer (`sql/`) turns a statement into reads and writes on the B+tree: a lexer cuts the text into tokens, a recursive descent parser builds a syntax tree, and the executor runs it. It supports `INT` and `TEXT` columns; the first column is the primary key.

A table is only a set of keys sharing a prefix:

```
schema: 0x01 <table>                    -> columns
row:    0x02 <table> 0x00 <primary key>  -> all the column values
```

Primary keys are encoded so that byte order matches value order (an `INT` gets its sign bit flipped and is written big-endian), so the rows of a table are stored next to each other, sorted by primary key. Conditions on the primary key joined by `AND` (`id = 7`, `id >= 10 AND id < 20`) narrow the scan to a key range; anything else scans the whole table. Each result shows which plan was used. Every statement is validated before it writes anything, so a failing statement changes nothing, even inside `BEGIN`.

## Transactions and concurrency

`BEGIN` opens a write transaction: its writes go to new pages kept in memory, and only that session sees them. `COMMIT` writes them all with a single pair of `fsync`s, so a transaction is all or nothing, even across a crash. `ROLLBACK` just drops the pages. Outside `BEGIN`, each write is its own transaction.

There is one writer at a time; a second one waits up to 5 seconds, then gets an error. Readers are never blocked by the writer: they read the last committed version, which copy-on-write leaves untouched. A commit only takes an exclusive lock for the in-memory switch to the new version, after its `fsync`s, and that lock waits for readers of the previous version, so a page they read can never be recycled under them.

## Layout

```
btree/       B+tree nodes, the tree, transactions, and the pager that stores it in a file
sql/         lexer, parser, planner and executor
repl/        one command line → one response
cmd/strata/  the interactive program
```
