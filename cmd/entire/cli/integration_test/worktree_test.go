//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/strategy"
	"github.com/go-git/go-git/v5/plumbing"
)

// TestWorktreeOpenRepository verifies that OpenRepository() works correctly
// in a worktree context by checking it can read HEAD and refs.
//
// NOTE: This test uses os.Chdir() so it cannot use t.Parallel().
func TestWorktreeOpenRepository(t *testing.T) {
	env := NewTestEnv(t)
	env.InitRepo()

	env.WriteFile("README.md", "# Test")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")

	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(worktreeDir)); err == nil {
		worktreeDir = filepath.Join(resolved, "worktree")
	}

	cmd := exec.Command("git", "worktree", "add", worktreeDir, "-b", "test-branch")
	cmd.Dir = env.RepoDir
	cmd.Env = gitIsolatedEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create worktree: %v\nOutput: %s", err, output)
	}

	originalWd, _ := os.Getwd()
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWd)
	})

	repo, err := strategy.OpenRepository(context.Background())
	if err != nil {
		t.Fatalf("OpenRepository() failed in worktree: %v", err)
	}

	head, err := repo.Head()
	if err != nil {
		t.Fatalf("repo.Head() failed: %v", err)
	}

	if head.Name().Short() != "test-branch" {
		t.Errorf("expected HEAD to be test-branch, got %s", head.Name().Short())
	}

	refs, err := repo.References()
	if err != nil {
		t.Fatalf("repo.References() failed: %v", err)
	}

	refCount := 0
	_ = refs.ForEach(func(ref *plumbing.Reference) error {
		refCount++
		return nil
	})

	if refCount == 0 {
		t.Error("expected to find refs, but found none")
	}

	t.Logf("Successfully opened worktree repo, HEAD=%s, found %d refs",
		head.Name().Short(), refCount)
}

func TestWorktree_UserPromptSubmitWithWorktreeConfig(t *testing.T) {
	t.Parallel()
	env := NewRepoWithCommit(t)

	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(worktreeDir)); err == nil {
		worktreeDir = filepath.Join(resolved, "worktree")
	}

	addWorktreeCmd := exec.Command("git", "worktree", "add", worktreeDir, "-b", "feature/worktreeconfig")
	addWorktreeCmd.Dir = env.RepoDir
	addWorktreeCmd.Env = gitIsolatedEnv()
	if output, err := addWorktreeCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create worktree: %v\nOutput: %s", err, output)
	}

	// Enable worktree config extension to reproduce the reported hook failure path.
	setConfigCmd := exec.Command("git", "config", "extensions.worktreeConfig", "true")
	setConfigCmd.Dir = worktreeDir
	setConfigCmd.Env = gitIsolatedEnv()
	if output, err := setConfigCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to set extensions.worktreeConfig: %v\nOutput: %s", err, output)
	}

	input := map[string]string{
		"session_id":      "worktree-config-session",
		"transcript_path": "",
		"prompt":          "hi",
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("failed to marshal hook input: %v", err)
	}

	hookCmd := exec.Command(getTestBinary(), "hooks", "claude-code", "user-prompt-submit")
	hookCmd.Dir = worktreeDir
	hookCmd.Stdin = bytes.NewReader(inputJSON)
	hookCmd.Env = append(gitIsolatedEnv(), "ENTIRE_TEST_CLAUDE_PROJECT_DIR="+env.ClaudeProjectDir)
	if output, err := hookCmd.CombinedOutput(); err != nil {
		t.Fatalf("user-prompt-submit failed: %v\nOutput: %s", err, output)
	}

	stateFile := filepath.Join(worktreeDir, ".entire", "tmp", "pre-prompt-worktree-config-session.json")
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("expected pre-prompt state file at %s: %v", stateFile, err)
	}
}

func TestWorktree_StopHookWithWorktreeConfig(t *testing.T) {
	t.Parallel()
	env := NewRepoWithCommit(t)

	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(worktreeDir)); err == nil {
		worktreeDir = filepath.Join(resolved, "worktree")
	}

	addWorktreeCmd := exec.Command("git", "worktree", "add", worktreeDir, "-b", "feature/stop-worktreeconfig")
	addWorktreeCmd.Dir = env.RepoDir
	addWorktreeCmd.Env = gitIsolatedEnv()
	if output, err := addWorktreeCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create worktree: %v\nOutput: %s", err, output)
	}

	setConfigCmd := exec.Command("git", "config", "extensions.worktreeConfig", "true")
	setConfigCmd.Dir = worktreeDir
	setConfigCmd.Env = gitIsolatedEnv()
	if output, err := setConfigCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to set extensions.worktreeConfig: %v\nOutput: %s", err, output)
	}

	// Ensure there is a modified file so the stop hook reaches git author lookup.
	if err := os.WriteFile(filepath.Join(worktreeDir, "created.txt"), []byte("created by claude"), 0o644); err != nil {
		t.Fatalf("failed to write created file: %v", err)
	}

	transcriptPath := filepath.Join(worktreeDir, ".entire", "tmp", "stop-worktreeconfig.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o755); err != nil {
		t.Fatalf("failed to create transcript dir: %v", err)
	}
	tb := NewTranscriptBuilder()
	tb.AddUserMessage("Create a file")
	tb.AddAssistantMessage("I'll help with that.")
	toolID := tb.AddToolUse("mcp__acp__Write", "created.txt", "created by claude")
	tb.AddToolResult(toolID)
	tb.AddAssistantMessage("Done!")
	if err := tb.WriteToFile(transcriptPath); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	input := map[string]string{
		"session_id":      "worktree-stop-session",
		"transcript_path": transcriptPath,
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("failed to marshal hook input: %v", err)
	}

	stopCmd := exec.Command(getTestBinary(), "hooks", "claude-code", "stop")
	stopCmd.Dir = worktreeDir
	stopCmd.Stdin = bytes.NewReader(inputJSON)
	stopCmd.Env = append(gitIsolatedEnv(), "ENTIRE_TEST_CLAUDE_PROJECT_DIR="+env.ClaudeProjectDir)
	if output, err := stopCmd.CombinedOutput(); err != nil {
		t.Fatalf("stop hook failed: %v\nOutput: %s", err, output)
	}
}
