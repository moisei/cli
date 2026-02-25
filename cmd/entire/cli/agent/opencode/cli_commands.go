package opencode

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// openCodeCommandTimeout is the maximum time to wait for opencode CLI commands.
const openCodeCommandTimeout = 30 * time.Second

// runOpenCodeExport runs `opencode export <sessionID>` to export a session
// from OpenCode's database. Returns the JSON export data as bytes.
func runOpenCodeExport(sessionID string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), openCodeCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "opencode", "export", sessionID)
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("opencode export timed out after %s", openCodeCommandTimeout)
		}
		// Get stderr for better error message
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("opencode export failed: %w (stderr: %s)", err, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("opencode export failed: %w", err)
	}

	return output, nil
}

// runOpenCodeSessionDelete runs `opencode session delete <sessionID>` to remove
// a session from OpenCode's database. Returns nil on success or if the session
// doesn't exist (nothing to delete).
func runOpenCodeSessionDelete(sessionID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), openCodeCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "opencode", "session", "delete", sessionID)
	if output, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("opencode session delete timed out after %s", openCodeCommandTimeout)
		}
		// "Session not found" means the session doesn't exist — nothing to delete.
		if strings.Contains(strings.ToLower(string(output)), "session not found") {
			return nil
		}
		return fmt.Errorf("opencode session delete failed: %w (output: %s)", err, string(output))
	}

	return nil
}

// runOpenCodeImport runs `opencode import <file>` to import a session into
// OpenCode's database. The import preserves the original session ID
// from the export file.
func runOpenCodeImport(exportFilePath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), openCodeCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "opencode", "import", exportFilePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("opencode import timed out after %s", openCodeCommandTimeout)
		}
		return fmt.Errorf("opencode import failed: %w (output: %s)", err, string(output))
	}

	return nil
}
