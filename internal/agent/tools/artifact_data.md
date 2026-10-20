Read and write an artifact's shared database: the same collections its page reads and writes through `window.tada.db`. The open page sees your writes live.

Actions: get, list, query (`query` is `{field: value}` equality), set (replaces a document; no `doc_id` creates one), update (merges `data`; null drops a field), delete, batch (`writes`, applied together), collections.

Use it to add or change rows in a tracker, log or dashboard instead of republishing the page.
