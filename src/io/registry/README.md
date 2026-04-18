# src/io/registry/

Rules-registry access, split-ownership to avoid merge conflicts:

| File | Owner |
|---|---|
| read.ts | M8 (read-latest-per-rule, status aggregation) |
| add.ts | M4 (append `added` events for new rules) |
| apply.ts | M6 (append `added`/`updated` on step70 adoption) |
| sunset.ts | M8 (status_changed / archived transitions) |
| index.ts | (free to add re-exports) |

All writers MUST compute monotonic version_seq + prev_hash per contracts.ts.
