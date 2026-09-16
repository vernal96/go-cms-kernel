# LibraryItem yearly partition maintenance

`core.library_items` is partitioned first by `HASH (library_id)` and then by
yearly `RANGE (partition_at)` partitions. The `*_default` partition under each
hash bucket accepts arbitrary historical and future timestamps, but it is only
a correctness fallback.

Before a new calendar year starts, infrastructure maintenance must create that
year's range partition under all eight hash buckets. Perform the following for
each bucket during a controlled maintenance window, with a backup and an
`ACCESS EXCLUSIVE` lock on `core.library_items_hN`:

1. Begin a transaction and detach `core.library_items_hN_default`.
2. Create `core.library_items_hN_yYYYY` for `[YYYY-01-01, YYYY+1-01-01)`.
3. Insert rows in that range from the detached default table into
   `core.library_items_hN`; PostgreSQL routes them to the new yearly partition.
4. Delete the copied rows from the detached default table and compare the
   inserted/deleted counts.
5. Attach the original table back to `core.library_items_hN` as `DEFAULT`, then
   commit.

Repeat this for buckets `h0` through `h7` and verify that no row in any DEFAULT
table has `partition_at` inside the newly provisioned range. The operation is
infrastructure-owned: never create partitions in an HTTP request, scheduler,
or ordinary LibraryItem write transaction.
