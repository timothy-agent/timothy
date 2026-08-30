# Choosing Database Indexes for Read Heavy Workloads

Adding an index is not free: every index speeds up the reads it
matches while slowing down every write to that table, and consumes
disk space that grows with the table. Choosing which columns to index
is a trade off, not a default to apply everywhere.

## Matching indexes to query patterns

An index only helps a query if it matches the columns used in the
query's filter or sort, in an order the planner can use. A composite
index on columns A and B helps a query filtering on A alone, or on A
and B together, but does not help a query filtering on B alone unless
a separate index exists for that access pattern.

## Over indexing

Tables with many indexes suffer on every insert and update, since each
index has to be maintained alongside the base table. A common failure
mode is adding a new index for every slow query reported in
production without ever removing indexes that later queries made
redundant, until the table's write path is dominated by index
maintenance rather than the actual write.

## Partial and covering indexes

A partial index, one that only covers rows matching a condition such
as an active flag, can be far smaller and faster than a full index
when the query only ever touches that subset of rows. A covering
index, one that includes every column a query needs so the planner
never has to touch the base table, trades index size for avoiding a
second lookup, which matters most for queries run at high frequency.

## Measuring before and after

Any indexing decision should be checked against the query planner's
actual execution plan before and after, since the planner sometimes
declines to use a new index for reasons that are not obvious from the
schema alone, such as the table being small enough that a sequential
scan is already cheaper.
