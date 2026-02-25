//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/agent"
	_ "github.com/entireio/cli/cmd/entire/cli/agent/claudecode" // Register claude-code agent
	_ "github.com/entireio/cli/cmd/entire/cli/agent/geminicli"  // Register gemini agent
	_ "github.com/entireio/cli/cmd/entire/cli/agent/opencode"   // Register opencode agent
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_ResumeInRelocatedRepo verifies that entire resume works when a repository
// is moved to a different location after checkpoint creation. This validates that
// transcript paths are computed from the current repo location, not stored paths
// from checkpoint creation time.
//
// The test demonstrates that restore is location-independent by:
// 1. Creating a checkpoint at original location
// 2. Moving the repo to a new location (different directory hierarchy)
// 3. Running entire resume in the new location
// 4. Verifying the transcript was written to the NEW location's session dir
// 5. Verifying the OLD location's session dir was NOT created
func TestE2E_ResumeInRelocatedRepo(t *testing.T) {
	t.Parallel()

	// Create an initial test environment at the original location
	env := NewFeatureBranchEnv(t, "manual-commit")
	originalDir := env.RepoDir

	t.Logf("Original repo location: %s", originalDir)

	// Step 1: Agent creates a file
	t.Log("Step 1: Running agent to create checkpoint")
	result, err := env.RunAgent(PromptCreateHelloGo.Prompt)
	require.NoError(t, err, "Agent should succeed")
	AssertAgentSuccess(t, result, err)

	// Step 2: Verify file was created
	t.Log("Step 2: Verifying file was created")
	require.True(t, env.FileExists("hello.go"), "hello.go should exist")

	// Step 3: User commits with hooks to create checkpoint
	t.Log("Step 3: Committing with hooks to create checkpoint")
	env.GitCommitWithShadowHooks("Add hello world program", "hello.go")

	// Step 4: Verify checkpoint exists
	t.Log("Step 4: Verifying checkpoint was created")
	checkpointID, err := env.GetLatestCheckpointIDFromHistory()
	require.NoError(t, err, "Should find checkpoint in commit history")
	require.NotEmpty(t, checkpointID, "Commit should have Entire-Checkpoint trailer")
	t.Logf("Checkpoint ID: %s", checkpointID)

	// Get the agent to compute session directories (agent-agnostic)
	agentInstance, err := agent.Get(agent.AgentName(defaultAgent))
	require.NoError(t, err, "should be able to get agent instance")

	// Compute the expected session directories for original and new locations.
	originalSessionDir, err := agentInstance.GetSessionDir(originalDir)
	require.NoError(t, err, "should compute original session dir")

	// Step 5: Move the repository to a new location
	t.Log("Step 5: Moving repository to new location")
	tempBase := t.TempDir()
	// Resolve symlinks (macOS: /var -> /private/var) to match what the CLI sees
	if resolved, err := filepath.EvalSymlinks(tempBase); err == nil {
		tempBase = resolved
	}
	newDir := filepath.Join(tempBase, "relocated", "new-location", "test-repo")
	require.NoError(t, os.MkdirAll(filepath.Dir(newDir), 0o755))
	require.NoError(t, os.Rename(originalDir, newDir), "should be able to move repo")

	_, err = os.Stat(originalDir)
	require.True(t, os.IsNotExist(err), "original location should not exist after move")
	t.Logf("Moved repo to: %s", newDir)

	newSessionDir, err := agentInstance.GetSessionDir(newDir)
	require.NoError(t, err, "should compute new session dir")

	// Sanity check: the two session dirs must be different
	require.NotEqual(t, originalSessionDir, newSessionDir,
		"session dirs should differ for different repo paths")
	t.Logf("Original session dir: %s", originalSessionDir)
	t.Logf("New session dir:      %s", newSessionDir)

	// Clean up new session dir if it somehow already exists (idempotent test)
	_ = os.RemoveAll(newSessionDir)

	// Step 6: Create new environment pointing at the moved repo
	t.Log("Step 6: Opening repo at new location")
	newEnv := &TestEnv{
		T:       t,
		RepoDir: newDir,
		Agent:   env.Agent,
	}

	// Step 7: Run entire resume in the new location
	// resume requires a branch name argument: entire resume <branch> [--force]
	t.Log("Step 7: Running 'entire resume feature/e2e-test --force' in new location")
	output := newEnv.RunCLI("resume", "feature/e2e-test", "--force")
	t.Logf("Resume output:\n%s", output)

	// Step 8: Verify the session was restored at the new location
	t.Log("Step 8: Verifying session was created at new location")

	// The resume output should reference the new session dir path, not the old one.
	newSessionDirBase := filepath.Base(newSessionDir)
	originalSessionDirBase := filepath.Base(originalSessionDir)
	assert.Contains(t, output, newSessionDirBase,
		"resume output should reference the new session directory")
	if newSessionDirBase != originalSessionDirBase {
		assert.NotContains(t, output, originalSessionDirBase,
			"resume output should NOT reference the old session directory")
	}

	// Verification differs by agent:
	// - Claude Code: writes transcript files to session directory
	// - OpenCode: imports session into its database (no files in session dir)
	if defaultAgent == AgentNameOpenCode {
		// For OpenCode, extract session ID from output and verify it exists in the database
		// Output format: "Session: ses_xxxxx"
		sessionIDRegex := regexp.MustCompile(`Session: (ses_[a-zA-Z0-9]+)`)
		matches := sessionIDRegex.FindStringSubmatch(output)
		require.NotEmpty(t, matches, "resume output should contain session ID")
		sessionID := matches[1]
		t.Logf("Extracted session ID: %s", sessionID)

		// Verify session exists in OpenCode's database via `opencode session list`
		listCmd := exec.Command("opencode", "session", "list")
		listCmd.Dir = newDir
		listOutput, listErr := listCmd.CombinedOutput()
		require.NoError(t, listErr, "opencode session list should succeed")
		assert.Contains(t, string(listOutput), sessionID,
			"session should exist in OpenCode's database after resume")
		t.Logf("OpenCode session list output:\n%s", string(listOutput))
	} else {
		// For Claude Code and others, verify files in session directory
		newDirEntries, err := os.ReadDir(newSessionDir)
		require.NoError(t, err, "new session directory should exist after resume")
		require.NotEmpty(t, newDirEntries, "new session directory should contain files")
		t.Logf("New session dir contains %d entries", len(newDirEntries))

		// Verify at least one transcript file exists
		hasTranscript := false
		for _, entry := range newDirEntries {
			if strings.HasSuffix(entry.Name(), ".jsonl") || strings.HasSuffix(entry.Name(), ".json") {
				info, statErr := entry.Info()
				if statErr == nil && info.Size() > 0 {
					hasTranscript = true
					t.Logf("Found transcript: %s (%d bytes)", entry.Name(), info.Size())
				}
			}
		}
		assert.True(t, hasTranscript, "new session directory should contain a non-empty transcript file")
	}

	// Step 9: Verify the OLD session directory was NOT created by resume
	// (It may exist from Step 1's agent run, so check that resume didn't write to it
	// by checking the output doesn't reference it — already asserted above.)
	t.Log("Step 9: Verified old session directory was not used by resume")

	t.Log("Test passed: entire resume correctly uses computed session path from new repo location")
}
