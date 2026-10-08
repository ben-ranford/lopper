# Advisory cache retention

`lopper advisory sync osv` retains snapshot metadata in `manifest.json` up to an
8 MiB serialized JSON budget. The budget includes JSON escaping, indentation,
separators, envelope fields, and the final newline, for both JSON and ZIP sources.

Each successful sync preserves its current snapshot record and updates `latest`.
If the combined metadata exceeds the budget, the cache removes historical records
in ascending retrieval-time order, breaking timestamp ties by snapshot ID.
Missing or invalid historical timestamps use the zero time for ordering. Retained
records remain ordered by snapshot ID. Syncing an existing digest refreshes its
record instead of adding a duplicate; an evicted digest can become current again.

Eviction removes metadata only. Historical snapshot files remain on disk, but
removed records are no longer listed in the manifest. This policy bounds manifest
metadata, not snapshot storage usage.

If the current snapshot's metadata alone cannot fit, sync reports a manifest-size
error, preserves the previous manifest, and rolls back a newly placed snapshot.
Existing snapshot files are preserved. Publication and retention share the
existing cache publication lock, so concurrent syncs cannot overwrite each other's
manifest updates. Atomic manifest replacement retains the prior file on failure.
