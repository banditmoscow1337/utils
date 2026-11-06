# Arena class-table update

Non-moving arenas now use an 80-byte physical block for payloads from 49 through
64 bytes after alignment. A 64-byte array therefore occupies 80 bytes, including
the allocator header, instead of 128. This is a normal reusable class: free-list
batching, claims, region reuse, Trim and diagnostics all account for it.

The original class indices and default allocation policy are unchanged. The new
class is appended at `Compact64Class`; `GetExtendedSizeClass` selects it for
non-moving allocations. The mapped Region remains exactly 256 bytes. Its private
free-head order keeps the 64-, 80- and 128-byte classes next to region claim
counters to reduce cache
line transfers on concurrent allocation. Availability masks and reported class
indices remain logical.

The availability table has one additional class, so persisted mappings use new
format identifiers: non-moving version 2 and default version 3. Prior formats
are rejected before allocator metadata is used. Existing arena files must be
rebuilt from their source data; this update does not implement file migration.
