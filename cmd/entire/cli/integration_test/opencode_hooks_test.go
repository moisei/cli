//go:build integration

package integration

import (
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/agent"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/strategy"
)

// TestOpenCodeHookFlow verifies the full hook flow for OpenCode:
// session-start → turn-start → file changes → turn-end → checkpoint → commit → condense → session-end.
func TestOpenCodeHookFlow(t *testing.T) {
	t.Parallel()

	RunForAllStrategies(t, func(t *testing.T, env *TestEnv, strategyName string) {
		env.InitEntireWithAgent(strategyName, agent.AgentNameOpenCode)

		// Create OpenCode session
		session := env.NewOpenCodeSession()

		// 1. session-start
		if err := env.SimulateOpenCodeSessionStart(session.ID, session.TranscriptPath); err != nil {
			t.Fatalf("session-start error: %v", err)
		}

		// 2. turn-start (equivalent to UserPromptSubmit — captures pre-prompt state)
		if err := env.SimulateOpenCodeTurnStart(session.ID, session.TranscriptPath, "Add a feature"); err != nil {
			t.Fatalf("turn-start error: %v", err)
		}

		// 3. Agent makes file changes (AFTER turn-start so they're detected as new)
		env.WriteFile("feature.go", "package main\n// new feature")

		// 4. Create transcript with the file change
		session.CreateOpenCodeTranscript("Add a feature", []FileChange{
			{Path: "feature.go", Content: "package main\n// new feature"},
		})

		// 5. turn-end (equivalent to Stop — creates checkpoint)
		if err := env.SimulateOpenCodeTurnEnd(session.ID, session.TranscriptPath); err != nil {
			t.Fatalf("turn-end error: %v", err)
		}

		// 6. Verify checkpoint was created
		points := env.GetRewindPoints()
		if len(points) == 0 {
			t.Fatal("expected at least 1 rewind point after turn-end")
		}

		// 7. For manual-commit, user commits manually (triggers condensation).
		// For auto-commit, the commit was already made during turn-end.
		if strategyName == strategy.StrategyNameManualCommit {
			env.GitCommitWithShadowHooks("Add feature", "feature.go")
		}

		// 8. session-end
		if err := env.SimulateOpenCodeSessionEnd(session.ID, session.TranscriptPath); err != nil {
			t.Fatalf("session-end error: %v", err)
		}

		// 9. Verify condensation happened (checkpoint on metadata branch)
		checkpointID := env.TryGetLatestCheckpointID()
		if checkpointID == "" {
			t.Fatal("expected checkpoint on metadata branch after commit")
		}

		// 10. Verify condensed data
		transcriptPath := SessionFilePath(checkpointID, paths.TranscriptFileName)
		_, found := env.ReadFileFromBranch(paths.MetadataBranchName, transcriptPath)
		if !found {
			t.Error("condensed transcript should exist on metadata branch")
		}
	})
}

// TestOpenCodeAgentStrategyComposition verifies that the OpenCode agent and strategy
// work together correctly — agent parses session, strategy saves checkpoint, rewind works.
func TestOpenCodeAgentStrategyComposition(t *testing.T) {
	t.Parallel()

	RunForAllStrategies(t, func(t *testing.T, env *TestEnv, strategyName string) {
		env.InitEntireWithAgent(strategyName, agent.AgentNameOpenCode)

		ag, err := agent.Get("opencode")
		if err != nil {
			t.Fatalf("Get(opencode) error = %v", err)
		}

		_, err = strategy.Get(strategyName)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", strategyName, err)
		}

		// Create session and transcript for agent interface testing.
		// The transcript references feature.go but the actual file doesn't need
		// to exist for ReadSession — it only parses the transcript JSONL.
		session := env.NewOpenCodeSession()
		transcriptPath := session.CreateOpenCodeTranscript("Add a feature", []FileChange{
			{Path: "feature.go", Content: "package main\n// new feature"},
		})

		// Read session via agent interface
		agentSession, err := ag.ReadSession(&agent.HookInput{
			SessionID:  session.ID,
			SessionRef: transcriptPath,
		})
		if err != nil {
			t.Fatalf("ReadSession() error = %v", err)
		}

		// Verify agent computed modified files
		if len(agentSession.ModifiedFiles) == 0 {
			t.Error("agent.ReadSession() should compute ModifiedFiles")
		}

		// Simulate session flow: session-start → turn-start → file changes → turn-end
		if err := env.SimulateOpenCodeSessionStart(session.ID, transcriptPath); err != nil {
			t.Fatalf("session-start error = %v", err)
		}
		if err := env.SimulateOpenCodeTurnStart(session.ID, transcriptPath, "Add a feature"); err != nil {
			t.Fatalf("turn-start error = %v", err)
		}

		// Create the actual file AFTER turn-start so the strategy detects it as new
		env.WriteFile("feature.go", "package main\n// new feature")

		if err := env.SimulateOpenCodeTurnEnd(session.ID, transcriptPath); err != nil {
			t.Fatalf("turn-end error = %v", err)
		}

		// Verify checkpoint was created
		points := env.GetRewindPoints()
		if len(points) == 0 {
			t.Fatal("expected at least 1 rewind point after turn-end")
		}
	})
}

// TestOpenCodeRewind verifies that rewind works with OpenCode checkpoints.
func TestOpenCodeRewind(t *testing.T) {
	t.Parallel()

	// Test with manual-commit strategy as it has full file restoration on rewind
	env := NewFeatureBranchEnv(t, strategy.StrategyNameManualCommit)
	env.InitEntireWithAgent(strategy.StrategyNameManualCommit, agent.AgentNameOpenCode)

	// First session
	session := env.NewOpenCodeSession()
	transcriptPath := session.TranscriptPath

	if err := env.SimulateOpenCodeSessionStart(session.ID, transcriptPath); err != nil {
		t.Fatalf("session-start error: %v", err)
	}

	// Turn 1: create file1.go (AFTER turn-start so it's detected as new)
	if err := env.SimulateOpenCodeTurnStart(session.ID, transcriptPath, "Create file1"); err != nil {
		t.Fatalf("turn-start error: %v", err)
	}

	env.WriteFile("file1.go", "package main\n// file1 v1")
	session.CreateOpenCodeTranscript("Create file1", []FileChange{
		{Path: "file1.go", Content: "package main\n// file1 v1"},
	})

	if err := env.SimulateOpenCodeTurnEnd(session.ID, transcriptPath); err != nil {
		t.Fatalf("turn-end error: %v", err)
	}

	points1 := env.GetRewindPoints()
	if len(points1) == 0 {
		t.Fatal("no rewind point after first turn")
	}
	checkpoint1ID := points1[0].ID

	// Turn 2: modify file1 and create file2 (AFTER turn-start)
	if err := env.SimulateOpenCodeTurnStart(session.ID, transcriptPath, "Modify file1"); err != nil {
		t.Fatalf("turn-start error: %v", err)
	}

	env.WriteFile("file1.go", "package main\n// file1 v2")
	env.WriteFile("file2.go", "package main\n// file2")
	session.CreateOpenCodeTranscript("Modify file1, create file2", []FileChange{
		{Path: "file1.go", Content: "package main\n// file1 v2"},
		{Path: "file2.go", Content: "package main\n// file2"},
	})

	if err := env.SimulateOpenCodeTurnEnd(session.ID, transcriptPath); err != nil {
		t.Fatalf("turn-end error: %v", err)
	}

	// Verify 2 checkpoints
	points2 := env.GetRewindPoints()
	if len(points2) < 2 {
		t.Fatalf("expected at least 2 rewind points, got %d", len(points2))
	}

	// Rewind to first checkpoint
	if err := env.Rewind(checkpoint1ID); err != nil {
		t.Fatalf("Rewind() error = %v", err)
	}

	// Verify file1 is restored to v1
	content := env.ReadFile("file1.go")
	if content != "package main\n// file1 v1" {
		t.Errorf("file1.go after rewind = %q, want v1 content", content)
	}

	// file2 should not exist after rewind to checkpoint 1
	if env.FileExists("file2.go") {
		t.Error("file2.go should not exist after rewind to checkpoint 1")
	}
}

// TestOpenCodeMultiTurnCondensation verifies that multiple turns in a session
// are correctly condensed when the user commits.
func TestOpenCodeMultiTurnCondensation(t *testing.T) {
	t.Parallel()

	env := NewFeatureBranchEnv(t, strategy.StrategyNameManualCommit)
	env.InitEntireWithAgent(strategy.StrategyNameManualCommit, agent.AgentNameOpenCode)

	session := env.NewOpenCodeSession()
	transcriptPath := session.TranscriptPath

	// session-start
	if err := env.SimulateOpenCodeSessionStart(session.ID, transcriptPath); err != nil {
		t.Fatalf("session-start error: %v", err)
	}

	// Turn 1: create file (AFTER turn-start so it's detected as new)
	if err := env.SimulateOpenCodeTurnStart(session.ID, transcriptPath, "Create app.go"); err != nil {
		t.Fatalf("turn-start error: %v", err)
	}

	env.WriteFile("app.go", "package main\nfunc main() {}")
	session.CreateOpenCodeTranscript("Create app.go", []FileChange{
		{Path: "app.go", Content: "package main\nfunc main() {}"},
	})

	if err := env.SimulateOpenCodeTurnEnd(session.ID, transcriptPath); err != nil {
		t.Fatalf("turn-end error: %v", err)
	}

	// Verify checkpoint
	points := env.GetRewindPoints()
	if len(points) == 0 {
		t.Fatal("expected rewind point after first turn")
	}

	// Commit with hooks (triggers condensation)
	env.GitCommitWithShadowHooks("Implement app", "app.go")

	// session-end
	if err := env.SimulateOpenCodeSessionEnd(session.ID, transcriptPath); err != nil {
		t.Fatalf("session-end error: %v", err)
	}

	// Verify checkpoint was condensed to metadata branch
	checkpointID := env.TryGetLatestCheckpointID()
	if checkpointID == "" {
		t.Fatal("expected checkpoint on metadata branch after commit")
	}

	// Verify files are on metadata branch
	env.ValidateCheckpoint(CheckpointValidation{
		CheckpointID: checkpointID,
		Strategy:     strategy.StrategyNameManualCommit,
		FilesTouched: []string{"app.go"},
		ExpectedTranscriptContent: []string{
			"Create app.go", // User prompt should appear in transcript
		},
	})
}

// TestOpenCodeMidTurnCommit verifies that when OpenCode's agent commits mid-turn
// (before turn-end), the commit gets an Entire-Checkpoint trailer AND the checkpoint
// data is written to entire/checkpoints/v1.
//
// This tests the PrepareTranscript fix: OpenCode's transcript file is created lazily
// at turn-end via `opencode export`. When a commit happens mid-turn, PrepareTranscript
// is called to create the transcript on-demand so condensation can read it.
func TestOpenCodeMidTurnCommit(t *testing.T) {
	t.Parallel()

	env := NewFeatureBranchEnv(t, strategy.StrategyNameManualCommit)
	env.InitEntireWithAgent(strategy.StrategyNameManualCommit, agent.AgentNameOpenCode)

	session := env.NewOpenCodeSession()

	// 1. session-start
	if err := env.SimulateOpenCodeSessionStart(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("session-start error: %v", err)
	}

	// 2. turn-start (session becomes ACTIVE)
	if err := env.SimulateOpenCodeTurnStart(session.ID, session.TranscriptPath, "Add a script and commit it"); err != nil {
		t.Fatalf("turn-start error: %v", err)
	}

	// 3. Agent creates file
	env.WriteFile("script.sh", "#!/bin/bash\necho hello")

	// 4. Create transcript reflecting the file change.
	session.CreateOpenCodeTranscript("Add a script and commit it", []FileChange{
		{Path: "script.sh", Content: "#!/bin/bash\necho hello"},
	})

	// 5. Copy transcript to .entire/tmp/ where PrepareTranscript will find it.
	// In production, `opencode export` refreshes this file on each call.
	// In tests, ENTIRE_TEST_OPENCODE_MOCK_EXPORT makes fetchAndCacheExport
	// read from the pre-written file at .entire/tmp/<sessionID>.json.
	// PrepareTranscript ALWAYS calls fetchAndCacheExport (even if file exists)
	// to ensure fresh data for resumed sessions.
	env.CopyTranscriptToEntireTmp(session.ID, session.TranscriptPath)

	// 6. Agent commits mid-turn (no turn-end yet!)
	// This triggers: PrepareCommitMsg (adds trailer) → PostCommit (runs condensation)
	// Condensation needs the transcript, which PrepareTranscript should provide.
	env.GitCommitWithShadowHooksAsAgent("Add script", "script.sh")

	// 7. Verify commit has checkpoint trailer
	commitHash := env.GetHeadHash()
	checkpointID := env.GetCheckpointIDFromCommitMessage(commitHash)
	if checkpointID == "" {
		t.Fatal("mid-turn agent commit should have Entire-Checkpoint trailer")
	}
	t.Logf("Mid-turn commit has checkpoint ID: %s", checkpointID)

	// 8. CRITICAL: Verify checkpoint data was written to entire/checkpoints/v1
	transcriptPath := SessionFilePath(checkpointID, paths.TranscriptFileName)
	_, found := env.ReadFileFromBranch(paths.MetadataBranchName, transcriptPath)
	if !found {
		t.Error("checkpoint transcript should exist on metadata branch after mid-turn commit")
	}

	// 9. Validate checkpoint metadata
	env.ValidateCheckpoint(CheckpointValidation{
		CheckpointID: checkpointID,
		Strategy:     strategy.StrategyNameManualCommit,
		FilesTouched: []string{"script.sh"},
	})
}

// TestOpenCodeResumedSessionAfterCommit verifies that resuming an OpenCode session
// after a commit correctly creates a checkpoint for the second turn.
//
// Scenario:
//  1. Turn 1: create new file → checkpoint → user commits (condensation)
//  2. Turn 2 (resumed): modify the now-tracked file → checkpoint should be created
func TestOpenCodeResumedSessionAfterCommit(t *testing.T) {
	t.Parallel()

	RunForAllStrategies(t, func(t *testing.T, env *TestEnv, strategyName string) {
		env.InitEntireWithAgent(strategyName, agent.AgentNameOpenCode)

		session := env.NewOpenCodeSession()
		transcriptPath := session.TranscriptPath

		// === Turn 1: Create a new file ===
		if err := env.SimulateOpenCodeSessionStart(session.ID, transcriptPath); err != nil {
			t.Fatalf("session-start error: %v", err)
		}
		if err := env.SimulateOpenCodeTurnStart(session.ID, transcriptPath, "Create app.go"); err != nil {
			t.Fatalf("turn-start 1 error: %v", err)
		}

		env.WriteFile("app.go", "package main\nfunc main() {}")
		session.CreateOpenCodeTranscript("Create app.go", []FileChange{
			{Path: "app.go", Content: "package main\nfunc main() {}"},
		})

		if err := env.SimulateOpenCodeTurnEnd(session.ID, transcriptPath); err != nil {
			t.Fatalf("turn-end 1 error: %v", err)
		}

		points1 := env.GetRewindPoints()
		if len(points1) == 0 {
			t.Fatal("expected rewind point after turn 1")
		}

		// === User commits (triggers condensation) ===
		// For auto-commit, turn-end already committed the file.
		// For manual-commit, user commits manually.
		if strategyName == strategy.StrategyNameManualCommit {
			env.GitCommitWithShadowHooks("Create app", "app.go")
		}

		// Verify condensation happened
		checkpointID := env.TryGetLatestCheckpointID()
		if checkpointID == "" {
			t.Fatal("expected checkpoint on metadata branch after commit")
		}

		// === Turn 2 (resumed): Modify the now-tracked file ===
		if err := env.SimulateOpenCodeTurnStart(session.ID, transcriptPath, "Add color output"); err != nil {
			t.Fatalf("turn-start 2 error: %v", err)
		}

		env.WriteFile("app.go", "package main\nimport \"fmt\"\nfunc main() { fmt.Println(\"hello\") }")
		session.CreateOpenCodeTranscript("Add color output", []FileChange{
			{Path: "app.go", Content: "package main\nimport \"fmt\"\nfunc main() { fmt.Println(\"hello\") }"},
		})

		if err := env.SimulateOpenCodeTurnEnd(session.ID, transcriptPath); err != nil {
			t.Fatalf("turn-end 2 error: %v", err)
		}

		// === Verify: a new checkpoint was created for turn 2 ===
		points2 := env.GetRewindPoints()
		if len(points2) == 0 {
			t.Fatal("expected rewind point after turn 2 (resumed session), got none")
		}

		// For manual-commit: commit turn 2 and verify second condensation
		if strategyName == strategy.StrategyNameManualCommit {
			env.GitCommitWithShadowHooks("Add color output", "app.go")

			checkpointID2 := env.TryGetLatestCheckpointID()
			if checkpointID2 == "" {
				t.Fatal("expected second checkpoint on metadata branch after turn 2 commit")
			}
			if checkpointID2 == checkpointID {
				t.Error("second checkpoint ID should differ from first")
			}
		}
	})
}
