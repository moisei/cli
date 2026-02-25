package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/entireio/cli/cmd/entire/cli/logging"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/strategy"
	"github.com/spf13/cobra"
)

func newCleanCmd() *cobra.Command {
	var forceFlag bool

	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Clean up orphaned Entire data",
		Long: `Remove orphaned Entire data (session state, shadow branches, checkpoint metadata, temp files) that wasn't cleaned up automatically.

This command finds and removes orphaned data from any strategy:

  Shadow branches (entire/<commit-hash>)
    Created by manual-commit strategy. Normally auto-cleaned when sessions
    are condensed during commits.

  Session state files (.git/entire-sessions/)
    Track active sessions. Orphaned when no checkpoints or shadow branches
    reference them.

  Checkpoint metadata (entire/checkpoints/v1 branch)
    For auto-commit checkpoints: orphaned when commits are rebased/squashed
    and no commit references the checkpoint ID anymore.
    Manual-commit checkpoints are permanent (condensed history) and are
    never considered orphaned.

  Temporary files (.entire/tmp/)
    Cached transcripts and other temporary data. Safe to delete when no
    active sessions are using them.

Default: shows a preview of items that would be deleted.
With --force, actually deletes the orphaned items.

The entire/checkpoints/v1 branch itself is never deleted.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runClean(cmd.OutOrStdout(), forceFlag)
		},
	}

	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "Actually delete items (default: dry run)")

	return cmd
}

func runClean(w io.Writer, force bool) error {
	// Initialize logging so structured logs go to .entire/logs/ instead of stderr.
	// Error is non-fatal: if logging init fails, logs go to stderr (acceptable fallback).
	logging.SetLogLevelGetter(GetLogLevel)
	if err := logging.Init(""); err == nil {
		defer logging.Close()
	}

	// List all cleanup items
	items, err := strategy.ListAllCleanupItems()
	if err != nil {
		return fmt.Errorf("failed to list orphaned items: %w", err)
	}

	// List temp files
	tempFiles, err := listTempFiles()
	if err != nil {
		// Non-fatal: continue with other cleanup items
		fmt.Fprintf(w, "Warning: failed to list temp files: %v\n", err)
	}

	return runCleanWithItems(w, force, items, tempFiles)
}

// listTempFiles returns files in .entire/tmp/ that are safe to delete,
// excluding files belonging to active sessions.
func listTempFiles() ([]string, error) {
	tmpDir, err := paths.AbsPath(paths.EntireTmpDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get temp dir path: %w", err)
	}

	entries, err := os.ReadDir(tmpDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read temp dir: %w", err)
	}

	// Build set of active session IDs to protect their temp files
	activeSessionIDs := make(map[string]bool)
	if states, listErr := strategy.ListSessionStates(); listErr == nil {
		for _, state := range states {
			activeSessionIDs[state.SessionID] = true
		}
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// Skip temp files belonging to active sessions (e.g., "session-id.json")
		name := entry.Name()
		sessionID := strings.TrimSuffix(name, ".json")
		if sessionID != name && activeSessionIDs[sessionID] {
			continue
		}
		files = append(files, name)
	}
	return files, nil
}

// TempFileDeleteError contains a file name and the error that occurred during deletion.
type TempFileDeleteError struct {
	File string
	Err  error
}

// deleteTempFiles removes all files in .entire/tmp/.
// Returns successfully deleted files and any failures with their error reasons.
func deleteTempFiles(files []string) (deleted []string, failed []TempFileDeleteError) {
	tmpDir, err := paths.AbsPath(paths.EntireTmpDir)
	if err != nil {
		// Can't get path - mark all as failed with the same error
		for _, file := range files {
			failed = append(failed, TempFileDeleteError{File: file, Err: err})
		}
		return nil, failed
	}

	for _, file := range files {
		path := filepath.Join(tmpDir, file)
		if err := os.Remove(path); err != nil {
			failed = append(failed, TempFileDeleteError{File: file, Err: err})
		} else {
			deleted = append(deleted, file)
		}
	}
	return deleted, failed
}

// runCleanWithItems is the core logic for cleaning orphaned items.
// Separated for testability.
func runCleanWithItems(w io.Writer, force bool, items []strategy.CleanupItem, tempFiles []string) error {
	// Handle no items case
	if len(items) == 0 && len(tempFiles) == 0 {
		fmt.Fprintln(w, "No orphaned items to clean up.")
		return nil
	}

	// Group items by type for display
	var branches, states, checkpoints []strategy.CleanupItem
	for _, item := range items {
		switch item.Type {
		case strategy.CleanupTypeShadowBranch:
			branches = append(branches, item)
		case strategy.CleanupTypeSessionState:
			states = append(states, item)
		case strategy.CleanupTypeCheckpoint:
			checkpoints = append(checkpoints, item)
		}
	}

	// Preview mode (default)
	if !force {
		totalItems := len(items) + len(tempFiles)
		fmt.Fprintf(w, "Found %d items to clean:\n\n", totalItems)

		if len(branches) > 0 {
			fmt.Fprintf(w, "Shadow branches (%d):\n", len(branches))
			for _, item := range branches {
				fmt.Fprintf(w, "  %s\n", item.ID)
			}
			fmt.Fprintln(w)
		}

		if len(states) > 0 {
			fmt.Fprintf(w, "Session states (%d):\n", len(states))
			for _, item := range states {
				fmt.Fprintf(w, "  %s\n", item.ID)
			}
			fmt.Fprintln(w)
		}

		if len(checkpoints) > 0 {
			fmt.Fprintf(w, "Checkpoint metadata (%d):\n", len(checkpoints))
			for _, item := range checkpoints {
				fmt.Fprintf(w, "  %s\n", item.ID)
			}
			fmt.Fprintln(w)
		}

		if len(tempFiles) > 0 {
			fmt.Fprintf(w, "Temp files (%d):\n", len(tempFiles))
			for _, file := range tempFiles {
				fmt.Fprintf(w, "  %s\n", file)
			}
			fmt.Fprintln(w)
		}

		fmt.Fprintln(w, "Run with --force to delete these items.")
		return nil
	}

	// Force mode - delete items
	result, err := strategy.DeleteAllCleanupItems(items)
	if err != nil {
		return fmt.Errorf("failed to delete orphaned items: %w", err)
	}

	// Delete temp files
	deletedTempFiles, failedTempFiles := deleteTempFiles(tempFiles)

	// Report results
	totalDeleted := len(result.ShadowBranches) + len(result.SessionStates) + len(result.Checkpoints) + len(deletedTempFiles)
	totalFailed := len(result.FailedBranches) + len(result.FailedStates) + len(result.FailedCheckpoints) + len(failedTempFiles)

	if totalDeleted > 0 {
		fmt.Fprintf(w, "Deleted %d items:\n", totalDeleted)

		if len(result.ShadowBranches) > 0 {
			fmt.Fprintf(w, "\n  Shadow branches (%d):\n", len(result.ShadowBranches))
			for _, branch := range result.ShadowBranches {
				fmt.Fprintf(w, "    %s\n", branch)
			}
		}

		if len(result.SessionStates) > 0 {
			fmt.Fprintf(w, "\n  Session states (%d):\n", len(result.SessionStates))
			for _, state := range result.SessionStates {
				fmt.Fprintf(w, "    %s\n", state)
			}
		}

		if len(result.Checkpoints) > 0 {
			fmt.Fprintf(w, "\n  Checkpoints (%d):\n", len(result.Checkpoints))
			for _, cp := range result.Checkpoints {
				fmt.Fprintf(w, "    %s\n", cp)
			}
		}

		if len(deletedTempFiles) > 0 {
			fmt.Fprintf(w, "\n  Temp files (%d):\n", len(deletedTempFiles))
			for _, file := range deletedTempFiles {
				fmt.Fprintf(w, "    %s\n", file)
			}
		}
	}

	if totalFailed > 0 {
		fmt.Fprintf(w, "\nFailed to delete %d items:\n", totalFailed)

		if len(result.FailedBranches) > 0 {
			fmt.Fprintf(w, "\n  Shadow branches:\n")
			for _, branch := range result.FailedBranches {
				fmt.Fprintf(w, "    %s\n", branch)
			}
		}

		if len(result.FailedStates) > 0 {
			fmt.Fprintf(w, "\n  Session states:\n")
			for _, state := range result.FailedStates {
				fmt.Fprintf(w, "    %s\n", state)
			}
		}

		if len(result.FailedCheckpoints) > 0 {
			fmt.Fprintf(w, "\n  Checkpoints:\n")
			for _, cp := range result.FailedCheckpoints {
				fmt.Fprintf(w, "    %s\n", cp)
			}
		}

		if len(failedTempFiles) > 0 {
			fmt.Fprintf(w, "\n  Temp files:\n")
			for _, fe := range failedTempFiles {
				fmt.Fprintf(w, "    %s: %v\n", fe.File, fe.Err)
			}
		}

		return fmt.Errorf("failed to delete %d items", totalFailed)
	}

	return nil
}
