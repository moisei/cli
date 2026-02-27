//go:build hookperf

package strategy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint/id"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/session"
	"github.com/entireio/cli/cmd/entire/cli/trailers"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

const hookPerfRepoURL = "https://github.com/entireio/cli.git"

// TestCommitHookPerformance measures the real overhead of Entire's commit hooks
// by comparing a control commit (no Entire) against a commit with hooks active.
//
// It uses a shallow clone of entireio/cli with seeded branches and packed refs
// to simulate a realistic repo, then loads session templates from the current
// repo's .git/entire-sessions/ to create authentic session state distributions.
//
// Prerequisites:
//   - GitHub access (gh auth login) for cloning the private repo
//   - At least one session state file in .git/entire-sessions/
//
// Run: go test -v -run TestCommitHookPerformance -tags hookperf -timeout 10m ./cmd/entire/cli/strategy/
func TestCommitHookPerformance(t *testing.T) {
	// Load session templates from the current repo before cloning.
	templates := loadSessionTemplates(t)

	// Clone once, reuse across scenarios via cheap local clones.
	cacheDir := cloneSourceRepo(t)

	scenarios := []struct {
		name   string
		ended  int
		idle   int
		active int
	}{
		{"100sessions", 88, 11, 1},
		{"200sessions", 176, 22, 2},
		{"500sessions", 440, 55, 5},
	}

	type result struct {
		name       string
		total      int
		control    time.Duration
		prepare    time.Duration
		postCommit time.Duration
	}
	results := make([]result, 0, len(scenarios))

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			totalSessions := sc.ended + sc.idle + sc.active

			dir := localClone(t, cacheDir)
			t.Chdir(dir)
			paths.ClearWorktreeRootCache()
			session.ClearGitCommonDirCache()

			// Seed 200 branches + pack refs for realistic ref scanning overhead.
			seedBranches(t, dir, 200)
			gitRun(t, dir, "pack-refs", "--all")

			// --- CONTROL: commit without Entire ---
			controlDur := timeControlCommit(t, dir)

			// Reset back to pre-commit state so the test commit is identical.
			gitRun(t, dir, "reset", "HEAD~1")
			gitRun(t, dir, "add", "perf_control.txt")

			// --- TEST: commit with Entire hooks ---
			createHookPerfSettings(t, dir)
			seedHookPerfSessions(t, dir, templates, sc.ended, sc.idle, sc.active)

			// Simulate TTY path with commit_linking=always.
			t.Setenv("ENTIRE_TEST_TTY", "1")
			paths.ClearWorktreeRootCache()
			session.ClearGitCommonDirCache()

			commitMsgFile := filepath.Join(dir, ".git", "COMMIT_EDITMSG")
			if err := os.WriteFile(commitMsgFile, []byte("implement feature\n"), 0o644); err != nil {
				t.Fatalf("write commit msg: %v", err)
			}

			s1 := &ManualCommitStrategy{}
			prepStart := time.Now()
			if err := s1.PrepareCommitMsg(context.Background(), commitMsgFile, "message"); err != nil {
				t.Fatalf("PrepareCommitMsg: %v", err)
			}
			prepDur := time.Since(prepStart)

			// Read back commit message; inject trailer if content-aware check skipped it.
			msgBytes, err := os.ReadFile(commitMsgFile) //nolint:gosec // test file
			if err != nil {
				t.Fatalf("read commit msg: %v", err)
			}
			commitMsg := string(msgBytes)

			if _, found := trailers.ParseCheckpoint(commitMsg); !found {
				cpID, genErr := id.Generate()
				if genErr != nil {
					t.Fatalf("generate checkpoint ID: %v", genErr)
				}
				commitMsg = fmt.Sprintf("%s\n%s: %s\n",
					strings.TrimRight(commitMsg, "\n"),
					trailers.CheckpointTrailerKey, cpID)
				t.Logf("  Injected trailer (PrepareCommitMsg skipped content-aware check)")
			}

			gitRun(t, dir, "commit", "-m", commitMsg)

			// Time PostCommit.
			paths.ClearWorktreeRootCache()
			session.ClearGitCommonDirCache()

			s2 := &ManualCommitStrategy{}
			postStart := time.Now()
			if err := s2.PostCommit(context.Background()); err != nil {
				t.Fatalf("PostCommit: %v", err)
			}
			postDur := time.Since(postStart)

			overhead := (prepDur + postDur) - controlDur
			if overhead < 0 {
				overhead = 0
			}

			t.Logf("=== %s ===", sc.name)
			t.Logf("  Sessions:         %d (ended=%d, idle=%d, active=%d)", totalSessions, sc.ended, sc.idle, sc.active)
			t.Logf("  Control commit:   %s", controlDur.Round(time.Millisecond))
			t.Logf("  PrepareCommitMsg: %s", prepDur.Round(time.Millisecond))
			t.Logf("  PostCommit:       %s", postDur.Round(time.Millisecond))
			t.Logf("  TOTAL HOOKS:      %s", (prepDur + postDur).Round(time.Millisecond))
			t.Logf("  OVERHEAD:         %s", overhead.Round(time.Millisecond))

			results = append(results, result{
				name:       sc.name,
				total:      totalSessions,
				control:    controlDur,
				prepare:    prepDur,
				postCommit: postDur,
			})
		})
	}

	// Print comparison table.
	t.Log("")
	t.Logf("Session templates: %d loaded from .git/entire-sessions/", len(templates))
	t.Log("")
	t.Log("========== COMMIT HOOK PERFORMANCE ==========")
	t.Logf("%-14s | %8s | %10s | %10s | %12s | %12s | %10s",
		"Scenario", "Sessions", "Control", "Prepare", "PostCommit", "Total+Hooks", "Overhead")
	t.Log(strings.Repeat("-", 95))
	for _, r := range results {
		total := r.prepare + r.postCommit
		overhead := total - r.control
		if overhead < 0 {
			overhead = 0
		}
		t.Logf("%-14s | %8d | %10s | %10s | %12s | %12s | %10s",
			r.name,
			r.total,
			r.control.Round(time.Millisecond),
			r.prepare.Round(time.Millisecond),
			r.postCommit.Round(time.Millisecond),
			total.Round(time.Millisecond),
			overhead.Round(time.Millisecond),
		)
	}
}

// sessionTemplate is a parsed session state file used as a template for seeding.
type sessionTemplate struct {
	state *session.State
}

// loadSessionTemplates reads .git/entire-sessions/*.json from the current repo
// and returns them as templates. Fatals if no templates are found.
func loadSessionTemplates(t *testing.T) []sessionTemplate {
	t.Helper()

	// Find the current repo's .git/entire-sessions/ directory.
	repoRoot, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	sessDir := filepath.Join(strings.TrimSpace(string(repoRoot)), ".git", session.SessionStateDirName)

	entries, err := os.ReadDir(sessDir)
	if err != nil {
		t.Fatalf("read %s: %v", sessDir, err)
	}

	var templates []sessionTemplate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(sessDir, entry.Name())) //nolint:gosec // test file
		if err != nil {
			t.Logf("  skip %s: %v", entry.Name(), err)
			continue
		}
		var state session.State
		if err := json.Unmarshal(data, &state); err != nil {
			t.Logf("  skip %s: %v", entry.Name(), err)
			continue
		}
		templates = append(templates, sessionTemplate{state: &state})
	}

	if len(templates) == 0 {
		t.Fatal("no session templates found in .git/entire-sessions/ — need at least one")
	}
	t.Logf("Loaded %d session templates from .git/entire-sessions/", len(templates))
	return templates
}

// timeControlCommit stages a file and times a bare `git commit` with no Entire
// hooks/settings present. Returns the wall-clock duration.
func timeControlCommit(t *testing.T, dir string) time.Duration {
	t.Helper()

	// Write and stage a file.
	controlFile := filepath.Join(dir, "perf_control.txt")
	if err := os.WriteFile(controlFile, []byte("control commit content\n"), 0o644); err != nil {
		t.Fatalf("write control file: %v", err)
	}
	gitRun(t, dir, "add", "perf_control.txt")

	// Time the commit.
	start := time.Now()
	gitRun(t, dir, "commit", "-m", "control commit (no Entire)")
	return time.Since(start)
}

// seedBranches creates N branches pointing at HEAD via go-git to simulate
// a repo with many refs (affects ref scanning performance).
func seedBranches(t *testing.T, dir string, count int) {
	t.Helper()

	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open repo for branch seeding: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head for branch seeding: %v", err)
	}
	headHash := head.Hash()

	for i := range count {
		name := fmt.Sprintf("feature/perf-branch-%03d", i)
		ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), headHash)
		if err := repo.Storer.SetReference(ref); err != nil {
			t.Fatalf("create branch %s: %v", name, err)
		}
	}
	t.Logf("  Seeded %d branches", count)
}

// cloneSourceRepo does a one-time shallow clone of entireio/cli into a temp
// directory. Returns the path to use as a local clone source for each scenario.
func cloneSourceRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	t.Logf("Cloning %s (depth=1) ...", hookPerfRepoURL)
	start := time.Now()

	//nolint:gosec // test-only, URL is a constant
	cmd := exec.Command("git", "clone", "--depth", "1", hookPerfRepoURL, dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git clone failed: %v\n%s", err, out)
	}
	t.Logf("Source clone completed in %s", time.Since(start).Round(time.Millisecond))

	return dir
}

// localClone creates a fast local clone from the cached source repo.
func localClone(t *testing.T, sourceDir string) string {
	t.Helper()

	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	//nolint:gosec // test-only, sourceDir is from t.TempDir()
	cmd := exec.Command("git", "clone", "--local", sourceDir, dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("local clone failed: %v\n%s", err, out)
	}

	return dir
}

// gitRun executes a git command in the given directory and fails the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	//nolint:gosec // test-only helper
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// createHookPerfSettings writes .entire/settings.json with commit_linking=always
// so PrepareCommitMsg auto-links without prompting.
func createHookPerfSettings(t *testing.T, dir string) {
	t.Helper()
	entireDir := filepath.Join(dir, ".entire")
	if err := os.MkdirAll(entireDir, 0o755); err != nil {
		t.Fatalf("mkdir .entire: %v", err)
	}
	settings := `{"enabled": true, "strategy": "manual-commit", "commit_linking": "always"}`
	if err := os.WriteFile(filepath.Join(entireDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

// seedHookPerfSessions creates session state files using templates from the
// current repo, duplicated round-robin to reach target counts.
//
// Phase distribution:
//
//	ENDED sessions: state file with LastCheckpointID (already condensed).
//	IDLE sessions:  state file + shadow branch checkpoint via SaveStep.
//	ACTIVE sessions: state file + shadow branch + live transcript file.
func seedHookPerfSessions(t *testing.T, dir string, templates []sessionTemplate, ended, idle, active int) {
	t.Helper()

	ctx := context.Background()

	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	baseCommit := head.Hash().String()

	worktreeID, err := paths.GetWorktreeID(dir)
	if err != nil {
		t.Fatalf("worktree ID: %v", err)
	}

	stateDir := filepath.Join(dir, ".git", session.SessionStateDirName)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	store := session.NewStateStoreWithDir(stateDir)

	modifiedFiles := []string{"main.go", "go.mod"}

	// --- Seed ENDED sessions (from templates, round-robin) ---
	for i := range ended {
		tmpl := templates[i%len(templates)]
		sessionID := fmt.Sprintf("perf-ended-%d", i)
		cpID := mustGenerateCheckpointID(t)
		now := time.Now()

		state := &session.State{
			SessionID:           sessionID,
			CLIVersion:          tmpl.state.CLIVersion,
			BaseCommit:          baseCommit,
			WorktreePath:        dir,
			WorktreeID:          worktreeID,
			Phase:               session.PhaseEnded,
			StartedAt:           now.Add(-time.Duration(i+1) * time.Hour),
			LastCheckpointID:    cpID,
			StepCount:           max(tmpl.state.StepCount, 1),
			FilesTouched:        modifiedFiles,
			LastInteractionTime: &now,
			AgentType:           tmpl.state.AgentType,
			TokenUsage:          tmpl.state.TokenUsage,
			FirstPrompt:         tmpl.state.FirstPrompt,
		}
		if err := store.Save(ctx, state); err != nil {
			t.Fatalf("save ended state %d: %v", i, err)
		}
	}

	// --- Seed IDLE sessions (with shadow branches) ---
	s := &ManualCommitStrategy{}
	for i := range idle {
		tmpl := templates[i%len(templates)]
		sessionID := fmt.Sprintf("perf-idle-%d", i)
		seedSessionWithShadowBranch(t, s, dir, sessionID, session.PhaseIdle, modifiedFiles)

		// Enrich state from template.
		state, loadErr := s.loadSessionState(ctx, sessionID)
		if loadErr != nil {
			t.Fatalf("load idle state %d: %v", i, loadErr)
		}
		state.AgentType = tmpl.state.AgentType
		state.TokenUsage = tmpl.state.TokenUsage
		state.FirstPrompt = tmpl.state.FirstPrompt
		if saveErr := s.saveSessionState(ctx, state); saveErr != nil {
			t.Fatalf("save idle state %d: %v", i, saveErr)
		}
	}

	// --- Seed ACTIVE sessions (shadow branch + live transcript) ---
	for i := range active {
		tmpl := templates[i%len(templates)]
		sessionID := fmt.Sprintf("perf-active-%d", i)
		seedSessionWithShadowBranch(t, s, dir, sessionID, session.PhaseActive, modifiedFiles)

		// Create a live transcript file.
		claudeProjectDir := filepath.Join(dir, ".claude", "projects", "test", "sessions")
		if err := os.MkdirAll(claudeProjectDir, 0o755); err != nil {
			t.Fatalf("mkdir claude sessions: %v", err)
		}
		transcript := `{"type":"human","message":{"content":"implement feature"}}
{"type":"assistant","message":{"content":"I'll implement that for you."}}
{"type":"tool_use","name":"write","input":{"path":"main.go","content":"package main\n// modified\nfunc main() {}\n"}}
`
		transcriptFile := filepath.Join(claudeProjectDir, sessionID+".jsonl")
		if err := os.WriteFile(transcriptFile, []byte(transcript), 0o644); err != nil {
			t.Fatalf("write live transcript: %v", err)
		}

		// Enrich state from template.
		state, loadErr := s.loadSessionState(ctx, sessionID)
		if loadErr != nil {
			t.Fatalf("load active state %d: %v", i, loadErr)
		}
		state.AgentType = tmpl.state.AgentType
		state.TokenUsage = tmpl.state.TokenUsage
		state.FirstPrompt = tmpl.state.FirstPrompt
		state.TranscriptPath = transcriptFile
		if saveErr := s.saveSessionState(ctx, state); saveErr != nil {
			t.Fatalf("save active state %d: %v", i, saveErr)
		}
	}

	// Verify seeded sessions.
	states, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list states: %v", err)
	}
	t.Logf("  Seeded %d session state files (expected %d)", len(states), ended+idle+active)
}

// seedSessionWithShadowBranch creates a session with a shadow branch checkpoint
// using SaveStep, then sets the desired phase.
func seedSessionWithShadowBranch(t *testing.T, s *ManualCommitStrategy, dir, sessionID string, phase session.Phase, modifiedFiles []string) {
	t.Helper()
	ctx := context.Background()

	for _, f := range modifiedFiles {
		abs := filepath.Join(dir, f)
		content := fmt.Sprintf("package main\n// modified by agent %s\nfunc f() {}\n", sessionID)
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	metadataDir := ".entire/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	if err := os.MkdirAll(metadataDirAbs, 0o755); err != nil {
		t.Fatalf("mkdir metadata: %v", err)
	}
	transcript := `{"type":"human","message":{"content":"implement feature"}}
{"type":"assistant","message":{"content":"I'll implement that for you."}}
`
	if err := os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(transcript), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	paths.ClearWorktreeRootCache()

	if err := s.SaveStep(ctx, StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  modifiedFiles,
		NewFiles:       []string{},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint 1",
		AuthorName:     "Perf",
		AuthorEmail:    "perf@test.com",
	}); err != nil {
		t.Fatalf("SaveStep %s: %v", sessionID, err)
	}

	state, err := s.loadSessionState(ctx, sessionID)
	if err != nil {
		t.Fatalf("load state %s: %v", sessionID, err)
	}
	state.Phase = phase
	state.FilesTouched = modifiedFiles
	if err := s.saveSessionState(ctx, state); err != nil {
		t.Fatalf("save state %s: %v", sessionID, err)
	}
}

func mustGenerateCheckpointID(t *testing.T) id.CheckpointID {
	t.Helper()
	cpID, err := id.Generate()
	if err != nil {
		t.Fatalf("generate checkpoint ID: %v", err)
	}
	return cpID
}
