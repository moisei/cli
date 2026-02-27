# Commit Hook Performance Analysis

## Test Results (2026-02-27)

Measured on a full-history single-branch clone of `entireio/cli` with 200 seeded branches and packed refs.
Each session generated with a unique base commit from repo history (89-441 unique base commits per scenario).

| Scenario | Sessions | Control | Prepare | PostCommit | Total | Overhead |
|----------|----------|---------|---------|------------|-------|----------|
| 100      | 100      | 29ms    | 165ms   | 172ms      | 337ms | 308ms    |
| 200      | 200      | 30ms    | 303ms   | 314ms      | 617ms | 587ms    |
| 500      | 500      | 30ms    | 743ms   | 773ms      | 1.52s | 1.49s    |

**Scaling: ~3ms per session, linear.** Control commit (no Entire) is ~30ms regardless of session count.

### Impact of test methodology

Earlier versions of this test had two issues that inflated the numbers:

1. **Shallow clone** (`--depth 1`): Produced a ~900KB packfile instead of realistic ~50-100MB. Understated object resolution costs by ~15%.

2. **Shared base commits**: All sessions used the same `BaseCommit` (HEAD), so `listAllSessionStates()` looked up the same shadow branch name hundreds of times. With unique base commits drawn from real repo history, the numbers dropped **~85%** — from ~21ms/session to ~3ms/session.

| Version | 100 sess | 200 sess | 500 sess | Per-session |
|---------|----------|----------|----------|-------------|
| Shallow + shared base | 1.74s | 3.59s | 9.52s | ~18ms |
| Full history + shared base | 2.00s | 4.16s | 10.9s | ~21ms |
| Full history + unique bases | 337ms | 617ms | 1.52s | ~3ms |

The shared-base test was unrealistic because `listAllSessionStates()` scanned the packed-refs file for the same nonexistent shadow branch ref on every session. With unique base commits, each lookup targets a different ref name, matching production behavior where sessions span many commits over time.

## How go-git `repo.Reference()` works

go-git has **no caching** for packed ref lookups. Each `repo.Reference()` call:
1. Tries to read a loose ref file (`.git/refs/heads/<name>`)
2. On miss, opens `packed-refs` and scans line-by-line until match or EOF
3. For refs that don't exist, scans the **entire** file every time

After `git pack-refs --all` (the default state after `git gc`), all refs are in packed-refs and loose ref files don't exist. This means every lookup scans the file.

## Scaling Dimensions

### 1. `repo.Reference()` — ref lookups (~1-2ms/session)

Every session triggers multiple git ref lookups via go-git's `repo.Reference()`:

| Call site | When | Per-session calls |
|-----------|------|-------------------|
| `listAllSessionStates()` (line 91) | Both hooks | 1× |
| `filterSessionsWithNewContent()` → `sessionHasNewContent()` (line 1131) | PrepareCommitMsg | 1× |
| `postCommitProcessSession()` (line 840) | PostCommit | 1× |

For ENDED sessions with `LastCheckpointID`, the orphan check at line 92 always passes (even when the ref doesn't exist), so the ref lookup cost is "wasted" work. These lookups dominate when base commits are shared (same ref scanned repeatedly), but with unique base commits the scan short-circuits at different positions.

PostCommit pre-resolves the shadow ref at line 840 and passes `cachedShadowTree` to avoid redundant lookups within that hook.

**Impact: ~1-2ms per session across both hooks combined.**

### 2. `store.List()` — session state file I/O (~0.5-1ms/session)

`StateStore.List()` does `os.ReadDir()` + `Load()` for every `.json` file in `.git/entire-sessions/`. Each `Load()` reads a file, parses JSON, runs `NormalizeAfterLoad()`, and checks staleness. Called once per hook via `listAllSessionStates()` → `findSessionsForWorktree()`.

**Impact: ~0.5-1ms per session.**

### 3. Transcript parsing — `countTranscriptItems()` (~0.5-1ms/session, conditional)

`sessionHasNewContent()` reads the transcript from the shadow branch tree and parses JSONL to count items. Only triggered for sessions that have a shadow branch (IDLE/ACTIVE, ~12% of sessions). ENDED sessions without shadow branches skip this entirely.

**Impact: ~0.5-1ms per session when triggered.**

### 4. Content overlap checks (~0.5-1ms/session, conditional)

`stagedFilesOverlapWithContent()` (PrepareCommitMsg) and `filesOverlapWithContent()` (PostCommit) compare staged/committed files against `FilesTouched`. Only triggered for sessions with both `FilesTouched` and relevant staged/committed files.

**Impact: ~0.5-1ms per session when triggered.**

## Cost Breakdown Per Session

| Operation | Cost | Calls | Subtotal |
|-----------|------|-------|----------|
| `repo.Reference()` | 0.5-1ms | 2-3× | 1-2ms |
| `store.Load()` (JSON parse) | 0.5-1ms | 1× | 0.5-1ms |
| `countTranscriptItems()` | 0.5-1ms | 0-1× | 0-1ms |
| Content overlap check | 0.5-1ms | 0-1× | 0-1ms |
| **Total** | | | **~2-5ms (avg ~3ms)** |

## Why It's Linear

The scaling is almost perfectly linear because:

- Both hooks iterate over **all** sessions (`listAllSessionStates()` → `findSessionsForWorktree()`)
- Each session independently triggers file I/O (state loading) and git operations (ref lookups)
- `listAllSessionStates()` does a `repo.Reference()` check for every session to detect orphans — even ENDED sessions that will never be condensed

## Optimization Opportunities

### High impact

1. **Skip orphan check for ENDED sessions with `LastCheckpointID`**: These sessions survive the check at line 92 anyway. Short-circuiting before `repo.Reference()` would eliminate ~88% of ref lookups in `listAllSessionStates()`.

2. **Prune stale ENDED sessions**: Sessions older than `StaleSessionThreshold` (7 days) are already cleaned up by `StateStore.Load()`. Aggressively pruning ENDED sessions that haven't been interacted with would reduce the iteration count.

### Medium impact

3. **Batch ref resolution**: Load all refs once into a map for O(1) lookups. Less impactful now that per-session ref cost is ~0.5-1ms, but still useful at scale.

4. **Cache shadow ref across hooks**: The ref resolved in `listAllSessionStates()` is thrown away and re-resolved in `filterSessionsWithNewContent()`. Threading it through would avoid redundant lookups.

### Low impact

5. **Use `CheckpointTranscriptStart` instead of re-parsing transcripts**: Avoid full JSONL parsing by comparing against a stored line count.

6. **Pack state files**: Single-file storage instead of one JSON per session to reduce `ReadDir()` + N file reads.

## Reproducing

```bash
go test -v -run TestCommitHookPerformance -tags hookperf -timeout 15m ./cmd/entire/cli/strategy/
```

Requires GitHub access for cloning. Sessions are generated from repo commit history (no external templates needed).
