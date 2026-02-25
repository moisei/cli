package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/session"
)

func TestInitHookLogging(t *testing.T) {
	// Create a temporary directory to simulate a git repo
	tmpDir := t.TempDir()

	// Change to temp dir (automatically restored after test)
	t.Chdir(tmpDir)

	// Initialize git repo (required for session state store to find .git common dir)
	gitInit := exec.CommandContext(context.Background(), "git", "init")
	gitInit.Dir = tmpDir
	if err := gitInit.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Run("returns cleanup func when no session state exists", func(t *testing.T) {
		// Create settings.json to indicate Entire is set up
		entireDir := filepath.Join(tmpDir, paths.EntireDir)
		if err := os.MkdirAll(entireDir, 0o755); err != nil {
			t.Fatalf("failed to create .entire directory: %v", err)
		}
		settingsFile := filepath.Join(entireDir, "settings.json")
		if err := os.WriteFile(settingsFile, []byte(`{"enabled":true}`), 0o644); err != nil {
			t.Fatalf("failed to create settings file: %v", err)
		}

		cleanup := initHookLogging()
		if cleanup == nil {
			t.Fatal("expected cleanup function, got nil")
		}
		cleanup() // Should not panic
	})

	t.Run("initializes logging when session state exists", func(t *testing.T) {
		// Create .entire directory
		entireDir := filepath.Join(tmpDir, paths.EntireDir)
		if err := os.MkdirAll(entireDir, 0o755); err != nil {
			t.Fatalf("failed to create .entire directory: %v", err)
		}

		// Create settings.json to indicate Entire is set up in this repo
		settingsFile := filepath.Join(entireDir, "settings.json")
		if err := os.WriteFile(settingsFile, []byte(`{"enabled":true,"strategy":"manual-commit"}`), 0o644); err != nil {
			t.Fatalf("failed to create settings file: %v", err)
		}

		// Create session state file in .git/entire-sessions/
		sessionID := "test-session-12345"
		stateDir := filepath.Join(tmpDir, ".git", session.SessionStateDirName)
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			t.Fatalf("failed to create session state directory: %v", err)
		}

		now := time.Now()
		state := session.State{
			SessionID:           sessionID,
			StartedAt:           now,
			LastInteractionTime: &now,
			Phase:               session.PhaseActive,
		}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("failed to marshal state: %v", err)
		}
		stateFile := filepath.Join(stateDir, sessionID+".json")
		if err := os.WriteFile(stateFile, data, 0o600); err != nil {
			t.Fatalf("failed to write session state file: %v", err)
		}
		defer os.Remove(stateFile)

		// Create logs directory (logging.Init will try to create the log file)
		logsDir := filepath.Join(entireDir, "logs")
		if err := os.MkdirAll(logsDir, 0o755); err != nil {
			t.Fatalf("failed to create logs directory: %v", err)
		}

		cleanup := initHookLogging()
		if cleanup == nil {
			t.Fatal("expected cleanup function, got nil")
		}
		defer cleanup()

		// Verify log file was created
		logFile := filepath.Join(logsDir, "entire.log")
		if _, err := os.Stat(logFile); os.IsNotExist(err) {
			t.Errorf("expected log file to be created at %s", logFile)
		}
	})
}

// TestInitHookLogging_SkipsWhenNotSetUp tests that initHookLogging() does not
// create .entire/logs/ in repos where Entire has not been set up.
// This is a separate test because it needs its own t.Chdir() to a different directory.
func TestInitHookLogging_SkipsWhenNotSetUp(t *testing.T) {
	// Create a temp directory without .entire/settings.json
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Initialize git repo
	gitInit := exec.CommandContext(context.Background(), "git", "init")
	gitInit.Dir = tmpDir
	if err := gitInit.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Do NOT create .entire/settings.json - simulating a repo where Entire is not set up

	cleanup := initHookLogging()
	if cleanup == nil {
		t.Fatal("expected cleanup function, got nil")
	}
	cleanup() // Should not panic

	// Verify .entire/logs was NOT created
	logsDir := filepath.Join(tmpDir, ".entire", "logs")
	if _, err := os.Stat(logsDir); !os.IsNotExist(err) {
		t.Errorf("expected .entire/logs to NOT be created when Entire is not set up, but it exists")
	}
}

// TestInitHookLogging_SkipsWhenDisabled tests that initHookLogging() does not
// create .entire/logs/ when Entire is set up but disabled.
func TestInitHookLogging_SkipsWhenDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Initialize git repo
	gitInit := exec.CommandContext(context.Background(), "git", "init")
	gitInit.Dir = tmpDir
	if err := gitInit.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create .entire/settings.json with enabled: false
	entireDir := filepath.Join(tmpDir, paths.EntireDir)
	if err := os.MkdirAll(entireDir, 0o755); err != nil {
		t.Fatalf("failed to create .entire directory: %v", err)
	}
	settingsFile := filepath.Join(entireDir, "settings.json")
	if err := os.WriteFile(settingsFile, []byte(`{"enabled":false,"strategy":"manual-commit"}`), 0o644); err != nil {
		t.Fatalf("failed to create settings file: %v", err)
	}

	cleanup := initHookLogging()
	if cleanup == nil {
		t.Fatal("expected cleanup function, got nil")
	}
	cleanup() // Should not panic

	// Verify .entire/logs was NOT created
	logsDir := filepath.Join(tmpDir, ".entire", "logs")
	if _, err := os.Stat(logsDir); !os.IsNotExist(err) {
		t.Errorf("expected .entire/logs to NOT be created when Entire is disabled, but it exists")
	}
}
