package opencode

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/agent"
	"github.com/entireio/cli/cmd/entire/cli/paths"
)

// Hook name constants — these become CLI subcommands under `entire hooks opencode`.
const (
	HookNameSessionStart = "session-start"
	HookNameSessionEnd   = "session-end"
	HookNameTurnStart    = "turn-start"
	HookNameTurnEnd      = "turn-end"
	HookNameCompaction   = "compaction"
)

// HookNames returns the hook verbs this agent supports.
func (a *OpenCodeAgent) HookNames() []string {
	return []string{
		HookNameSessionStart,
		HookNameSessionEnd,
		HookNameTurnStart,
		HookNameTurnEnd,
		HookNameCompaction,
	}
}

// ParseHookEvent translates OpenCode hook calls into normalized lifecycle events.
func (a *OpenCodeAgent) ParseHookEvent(hookName string, stdin io.Reader) (*agent.Event, error) {
	switch hookName {
	case HookNameSessionStart:
		raw, err := agent.ReadAndParseHookInput[sessionInfoRaw](stdin)
		if err != nil {
			return nil, err
		}
		return &agent.Event{
			Type:      agent.SessionStart,
			SessionID: raw.SessionID,
			Timestamp: time.Now(),
		}, nil

	case HookNameTurnStart:
		raw, err := agent.ReadAndParseHookInput[turnStartRaw](stdin)
		if err != nil {
			return nil, err
		}
		// Get the temp file path for this session (may not exist yet, but needed for pre-prompt state).
		repoRoot, err := paths.WorktreeRoot()
		if err != nil {
			repoRoot = "."
		}
		tmpDir := filepath.Join(repoRoot, paths.EntireTmpDir)
		transcriptPath := filepath.Join(tmpDir, raw.SessionID+".json")
		return &agent.Event{
			Type:       agent.TurnStart,
			SessionID:  raw.SessionID,
			SessionRef: transcriptPath,
			Prompt:     raw.Prompt,
			Timestamp:  time.Now(),
		}, nil

	case HookNameTurnEnd:
		raw, err := agent.ReadAndParseHookInput[sessionInfoRaw](stdin)
		if err != nil {
			return nil, err
		}
		// Call `opencode export` to get the transcript and write to temp file
		transcriptPath, exportErr := a.fetchAndCacheExport(raw.SessionID)
		if exportErr != nil {
			return nil, fmt.Errorf("failed to export session: %w", exportErr)
		}
		return &agent.Event{
			Type:       agent.TurnEnd,
			SessionID:  raw.SessionID,
			SessionRef: transcriptPath,
			Timestamp:  time.Now(),
		}, nil

	case HookNameCompaction:
		raw, err := agent.ReadAndParseHookInput[sessionInfoRaw](stdin)
		if err != nil {
			return nil, err
		}
		return &agent.Event{
			Type:      agent.Compaction,
			SessionID: raw.SessionID,
			Timestamp: time.Now(),
		}, nil

	case HookNameSessionEnd:
		raw, err := agent.ReadAndParseHookInput[sessionInfoRaw](stdin)
		if err != nil {
			return nil, err
		}
		return &agent.Event{
			Type:      agent.SessionEnd,
			SessionID: raw.SessionID,
			Timestamp: time.Now(),
		}, nil

	default:
		return nil, nil //nolint:nilnil // nil event = no lifecycle action for unknown hooks
	}
}

// PrepareTranscript ensures the OpenCode transcript file is up-to-date by calling `opencode export`.
// OpenCode's transcript is created/updated via `opencode export`, but condensation may need fresh
// data mid-turn (e.g., during mid-turn commits or resumed sessions where the cached file is stale).
// This method always refreshes the transcript to ensure the latest agent activity is captured.
func (a *OpenCodeAgent) PrepareTranscript(sessionRef string) error {
	// Validate the session ref path
	if _, err := os.Stat(sessionRef); err != nil && !os.IsNotExist(err) {
		// Permission denied, broken symlink, or other non-recoverable errors
		return fmt.Errorf("failed to stat OpenCode transcript path %s: %w", sessionRef, err)
	}

	// Extract session ID from path: basename without .json extension
	base := filepath.Base(sessionRef)
	if !strings.HasSuffix(base, ".json") {
		return fmt.Errorf("invalid OpenCode transcript path (expected .json): %s", sessionRef)
	}
	sessionID := strings.TrimSuffix(base, ".json")
	if sessionID == "" {
		return fmt.Errorf("empty session ID in transcript path: %s", sessionRef)
	}

	// Always call fetchAndCacheExport to get fresh transcript data.
	// This is critical for resumed sessions where the cached file may contain stale data
	// from a previous turn. Unlike turn-end (which always runs export), mid-turn commits
	// need to refresh the transcript to capture agent activity since the last export.
	_, err := a.fetchAndCacheExport(sessionID)
	return err
}

// fetchAndCacheExport calls `opencode export <sessionID>` and writes the result
// to a temporary file. Returns the path to the temp file.
//
// Integration testing: Set ENTIRE_TEST_OPENCODE_MOCK_EXPORT=1 to skip the
// `opencode export` call and use pre-written mock data instead. Tests must
// pre-write the transcript file to .entire/tmp/<sessionID>.json before
// triggering the hook. See integration_test/hooks.go:SimulateOpenCodeTurnEnd.
func (a *OpenCodeAgent) fetchAndCacheExport(sessionID string) (string, error) {
	// Get worktree root for the temp directory
	repoRoot, err := paths.WorktreeRoot()
	if err != nil {
		repoRoot = "."
	}

	tmpDir := filepath.Join(repoRoot, paths.EntireTmpDir)
	tmpFile := filepath.Join(tmpDir, sessionID+".json")

	// Integration test mode: use pre-written mock file without calling opencode export
	if os.Getenv("ENTIRE_TEST_OPENCODE_MOCK_EXPORT") != "" {
		if _, err := os.Stat(tmpFile); err == nil {
			return tmpFile, nil
		}
		return "", fmt.Errorf("mock export file not found: %s (ENTIRE_TEST_OPENCODE_MOCK_EXPORT is set)", tmpFile)
	}

	// Call opencode export to get the transcript (always refresh on each turn)
	data, err := runOpenCodeExport(sessionID)
	if err != nil {
		return "", fmt.Errorf("opencode export failed: %w", err)
	}

	// Validate output is valid JSON before caching
	if !json.Valid(data) {
		return "", fmt.Errorf("opencode export returned invalid JSON (%d bytes)", len(data))
	}

	// Write to temp directory under .entire
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}

	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return "", fmt.Errorf("failed to write export file: %w", err)
	}

	return tmpFile, nil
}
