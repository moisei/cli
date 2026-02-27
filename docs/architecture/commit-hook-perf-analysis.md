# Commit Hook Performance Analysis

## Test Results (2026-02-27)

Measured on a full-history single-branch clone of `entireio/cli` with 200 seeded branches and packed refs.
12 session templates loaded from `.git/entire-sessions/` and duplicated round-robin.

| Scenario | Sessions | Control | Prepare | PostCommit | Total | Overhead |
|----------|----------|---------|---------|------------|-------|----------|
| 100      | 100      | 20ms    | 1.01s   | 984ms      | 2.00s | 1.98s    |
| 200      | 200      | 30ms    | 2.09s   | 2.07s      | 4.16s | 4.13s    |
| 500      | 500      | 30ms    | 5.45s   | 5.49s      | 10.9s | 10.9s    |

**Scaling: ~21ms per session, linear.** Control commit (no Entire) is ~20-30ms regardless of session count.

### Shallow vs full-history clone comparison

An earlier version used `--depth 1` (shallow clone), which produced a ~900KB object database instead of the realistic ~50-100MB packfile. This understated go-git object resolution costs by ~15%:

| Scenario | Shallow clone | Full history | Delta |
|----------|---------------|--------------|-------|
| 100 sess | 1.74s         | 2.00s        | +15%  |
| 200 sess | 3.59s         | 4.16s        | +16%  |
| 500 sess | 9.52s         | 10.9s        | +15%  |

The difference comes from `tree.File()`, `commit.Tree()`, and `file.Contents()` operating on a larger packfile index. Ref resolution (`repo.Reference()`) is unaffected since packed-refs count is the same.

## Scaling Dimensions

### 1. `repo.Reference()` — the dominant cost (~10-12ms/session)

Every session triggers multiple git ref lookups via go-git's `repo.Reference()`:

| Call site | When | Per-session calls |
|-----------|------|-------------------|
| `listAllSessionStates()` (line 91) | Both hooks | 1× |
| `filterSessionsWithNewContent()` → `sessionHasNewContent()` (line 1131) | PrepareCommitMsg | 1× |
| `postCommitProcessSession()` (line 840) | PostCommit | 1× |
| `sessionHasNewContent()` in PostCommit (line 1131) | PostCommit (non-ACTIVE) | 1× |

That's **2 calls per session in PrepareCommitMsg** and **2-3 in PostCommit**. Each call costs ~4-5ms because go-git iterates through refs rather than doing a hash-map lookup. With 200 packed branches, this is measurable.

Note: PostCommit pre-resolves the shadow ref at line 840 and passes `cachedShadowTree` to `sessionHasNewContent()`, so the second lookup is avoided for sessions that hit that path. But `listAllSessionStates()` at line 91 always does a fresh lookup for every session.

**Impact: ~10-12ms per session across both hooks combined.**

### 2. Transcript parsing — `countTranscriptItems()` (~2-3ms/session)

`sessionHasNewContent()` reads the transcript from the shadow branch tree and parses every JSONL line to count items (line 1159):

```
tree.File(metadataDir + "/full.jsonl")  → file.Contents() → countTranscriptItems()
```

This happens once per session in PrepareCommitMsg (`filterSessionsWithNewContent`) and once in PostCommit (`sessionHasNewContent` for non-ACTIVE sessions). The cost scales with transcript size — our test uses small transcripts (~3 lines), so real-world cost could be higher for sessions with large transcripts.

**Impact: ~2-3ms per session.**

### 3. `store.List()` — session state file I/O (~1-2ms/session)

`StateStore.List()` does `os.ReadDir()` + `Load()` for every `.json` file in `.git/entire-sessions/`. Each `Load()` reads a file, parses JSON, runs `NormalizeAfterLoad()`, and checks staleness. This is called once per hook via `listAllSessionStates()` → `findSessionsForWorktree()`.

**Impact: ~1-2ms per session.**

### 4. Tree traversal — `tree.File()` (~2-3ms/session)

go-git's `tree.File()` walks the git tree object to find the transcript file under `.entire/metadata/<session-id>/full.jsonl`. This involves resolving subtree objects for each path component from the packfile. With a full-history packfile (~50-100MB), index lookups are slower than with a shallow clone's ~900KB packfile. Called once per session in the content-check path.

**Impact: ~2-3ms per session.**

### 5. Content overlap checks (~3-5ms/session, conditional)

`stagedFilesOverlapWithContent()` (PrepareCommitMsg) and `filesOverlapWithContent()` (PostCommit) compare staged/committed files against the session's `FilesTouched` list. These involve reading tree entries and comparing blob hashes. Only triggered for sessions with `FilesTouched` and no transcript — which is most sessions in carry-forward scenarios.

**Impact: ~3-5ms per session when triggered.**

## Cost Breakdown Per Session

| Operation | Cost | Calls | Subtotal |
|-----------|------|-------|----------|
| `repo.Reference()` | 4-5ms | 2-3× | 8-15ms |
| `countTranscriptItems()` | 2-3ms | 1× | 2-3ms |
| `tree.File()` traversal | 2-3ms | 1× | 2-3ms |
| `store.Load()` (JSON parse) | 1-2ms | 1× | 1-2ms |
| Content overlap check | 3-5ms | 0-1× | 0-5ms |
| **Total** | | | **~16-28ms (avg ~21ms)** |

## Why It's Linear

The scaling is almost perfectly linear because:

- Both hooks iterate over **all** sessions (`listAllSessionStates()` → `findSessionsForWorktree()`)
- Each session independently triggers expensive git operations with no cross-session caching
- `listAllSessionStates()` does a `repo.Reference()` check for every session to detect orphans — even ENDED sessions that will never be condensed
- `filterSessionsWithNewContent()` re-resolves the shadow branch ref that `listAllSessionStates()` already checked

## Optimization Opportunities

### High impact

1. **Batch ref resolution in `listAllSessionStates()`**: Load all refs once into a map, then do O(1) lookups per session. Eliminates ~4-5ms × N from the first loop.

2. **Cache shadow ref across `listAllSessionStates()` → `filterSessionsWithNewContent()`**: The ref resolved at line 91 is thrown away and re-resolved at line 1131. Threading it through would save ~4-5ms × N.

3. **Skip orphan cleanup for ENDED sessions with `LastCheckpointID`**: These sessions survive the orphan check anyway (line 92), so the `repo.Reference()` call is wasted. Short-circuit before the ref lookup.

### Medium impact

4. **Use `CheckpointTranscriptStart` instead of re-parsing transcripts**: The session state already tracks the transcript offset. Comparing it against the shadow branch commit count or a stored line count would avoid full JSONL parsing.

5. **Lazy content checks**: Only call `sessionHasNewContent()` for sessions whose `FilesTouched` overlaps with staged/committed files. Skip sessions that can't possibly match.

### Low impact

6. **Parallel session processing**: Process sessions concurrently in the PostCommit loop (condensation is independent per session).

7. **Pack state files**: Instead of one JSON file per session, use a single file with all session states to reduce `ReadDir()` + N file reads to one read.

## Reproducing

```bash
go test -v -run TestCommitHookPerformance -tags hookperf -timeout 15m ./cmd/entire/cli/strategy/
```

Requires GitHub access for cloning and at least one session state file in `.git/entire-sessions/`.
