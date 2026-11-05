# Shared mmap arena

The allocator has one implementation in `utils.Arena`. This package contains
type aliases and constructors selecting its non-moving reclamation policy.
There is no interface dispatch or wrapper per allocation.

| API | Default utils policy | Non-moving policy |
|---|---|---|
| Constructor | `utils.NewArena[T](size, fd)` | `arena.New(size)` / `arena.Open(size, fd)` |
| Full-block classes | Original 9, up to 8192 bytes | 18, including 8256, 16448 and full regions |
| Sparse live regions | Evacuation hint; free lists excluded | Remain available for class reuse |
| Empty regions | Reused; automatic page release | Reused; page release through `Trim` |
| Region search | Tight reverse scan; bitmap after 256 initialized regions | Availability bitmap with direct newest-region lookup |
| Size classifier | `utils.GetSizeClass` retains original contract | `arena.GetSizeClass` / `utils.GetExtendedSizeClass` |
| Diagnostics | `Stats`, `Trim`, `PayloadCapacity`, `CheckedPtr` | Same methods |

`utils.NewArenaWithOptions(size, fd, utils.ArenaOptions{NonMoving:true})`
selects the same policy as this package. Neither policy moves a live object:
the default evacuation flag is only a hint for clients implementing relocation.

## Contracts

Offsets are uint64, aligned to eight bytes; zero is nil. Objects contain no
Go heap pointers. The maximum complete block, including its 16-byte header,
fits in a 2-MiB region. `Alloc` returns dirty memory; `AllocZeroed(n)` clears
the requested payload bytes. Non-moving allocations round to extended classes;
default allocations retain the original rounding for larger requests.

`Free` requires ownership and no remaining readers. `GetPtr` remains the
original cheap, unchecked pointer resolution. `CheckedPtr` checks mapping
bounds; `PayloadCapacity` additionally checks the owned block header. Neither
proves liveness or the provenance of forged offsets. Close requires quiescence.

`AllocBatch` is one exact block with explicit claims; return excess claims with
`AddActiveCount` before its final Free. `AllocChunk` returns raw space for
caller-written headers; blocks must cover its charged bytes and claims exactly.
Default arenas build their class index once they initialize more than 256 regions
(roughly 512 MiB of region space). Index publication is synchronized with concurrent
Free and PopBatch, and file reopening rebuilds it from the owned free chains.
Small default arenas keep their tight scan. Empty regions reset their cursor and can serve allocations beyond the cumulative
size of the mapping. Exact holes in partially live regions are not coalesced.

Idle TLAB reservations are recoverable even after sync.Pool drops its entries:
Trim, Close and OOM recovery drain the registered idle TLABs. Busy TLABs remain
owned. Trim preserves metadata pages and live objects, returns madvise errors,
and retries unsuccessful page release on a later call.

Stats is an approximate diagnostic snapshot. It walks free chains under their
head locks, keeping per-block diagnostic counters off Alloc/Free. It belongs at
batch boundaries, not inside a query. Physical RSS is separate from claimed
bytes, which include headers, class rounding and reserved TLAB tails.

## Files and compatibility

**The default utils mmap layout changes to version 2. Existing original utils
files are rejected rather than interpreted as the new region table.** Rebuild
those arenas from application data. The Go constructor/API remains available.

The non-moving reader accepts existing adaptive format-1 mappings after orderly
Close, preserving payload offsets. Downgrading to the old allocator is not a
supported file protocol. Reopen with the same policy and mapping size. Mapped
metadata is fixed at 128 bytes plus 256-byte region records and class bitmaps on
the supported 64-bit targets. Concurrent owners of one file, crash recovery,
durable transactions and application graph recovery are outside this API.

## Verification and performance

The shared ownership, reclamation, file, wide-offset, race, checkptr and fuzz
tests live in utils. Adaptive tests the object graphs against this package.
See the [comparison report](../docs/arena-consolidation.md) and the reproducible
[benchmark runner](../scripts/arena-compare/run.py). Historical sources are
materialized only in a temporary benchmark module, never maintained as a
second allocator in the active packages.
