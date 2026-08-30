# Chunking Strategy for Retrieval Augmented Generation

The way a document is split into chunks before embedding has more
impact on retrieval quality than most teams expect. Chunk boundaries
that cut a definition in half, or separate a claim from its
supporting example, hurt recall even when the embedding model itself
is strong.

## Fixed size versus structural chunking

Fixed size chunking, splitting every document into equal token
windows, is simple to implement but ignores document structure. A
heading and its first paragraph can end up in different chunks,
which weakens the semantic signal for a query that matches the
heading's topic. Structural chunking, splitting along headings and
paragraph boundaries, keeps a section's context together at the cost
of variable chunk sizes.

## Overlap and breadcrumbs

Adding a small overlap between adjacent chunks, or prepending a
breadcrumb of the enclosing headings to each chunk's text, recovers
context that a hard boundary would otherwise lose. A breadcrumb costs
almost nothing at embedding time and meaningfully improves recall for
queries that depend on knowing which section a chunk came from.

## Chunk size trade offs

Very small chunks, a sentence or two, tend to produce embeddings too
narrow to match a broader query, and flood a search result with near
duplicate hits from the same document. Very large chunks dilute the
embedding with unrelated content and reduce precision. A few hundred
words per chunk is a reasonable default for prose documents, with
adjustments for tables and code blocks which behave differently under
either scheme.

## Testing chunking changes

Any change to a chunking strategy should be evaluated against a fixed
set of representative queries with known expected documents, since
chunking interacts with the embedding model, the fusion method, and
the similarity floor in ways that are hard to predict from theory
alone. A regression in recall after a chunking change is easy to miss
without this kind of harness.
