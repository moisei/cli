package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/entireio/cli/cmd/entire/cli/agent"
)

// Compile-time interface assertions
var (
	_ agent.TranscriptAnalyzer = (*OpenCodeAgent)(nil)
	_ agent.TranscriptPreparer = (*OpenCodeAgent)(nil)
	_ agent.TokenCalculator    = (*OpenCodeAgent)(nil)
)

// ParseExportSession parses export JSON content into an ExportSession structure.
func ParseExportSession(data []byte) (*ExportSession, error) {
	if len(data) == 0 {
		return nil, nil //nolint:nilnil // nil for empty data is expected
	}

	var session ExportSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse export session: %w", err)
	}

	return &session, nil
}

// parseExportSessionFromFile reads and parses an export JSON transcript file.
func parseExportSessionFromFile(path string) (*ExportSession, error) {
	data, err := os.ReadFile(path) //nolint:gosec // Path from agent hook
	if err != nil {
		return nil, err //nolint:wrapcheck // Callers check os.IsNotExist on this error
	}
	return ParseExportSession(data)
}

// SliceFromMessage returns an OpenCode export transcript scoped to messages starting from
// startMessageIndex. This is the OpenCode equivalent of transcript.SliceFromLine —
// for OpenCode's JSON format, scoping is done by message index rather than line offset.
// Returns the original data if startMessageIndex <= 0.
// Returns nil, nil if startMessageIndex exceeds the number of messages.
func SliceFromMessage(data []byte, startMessageIndex int) ([]byte, error) {
	if len(data) == 0 || startMessageIndex <= 0 {
		return data, nil
	}

	session, err := ParseExportSession(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse export session for slicing: %w", err)
	}
	if session == nil {
		return nil, nil
	}

	if startMessageIndex >= len(session.Messages) {
		return nil, nil
	}

	scoped := &ExportSession{
		Info:     session.Info,
		Messages: session.Messages[startMessageIndex:],
	}

	out, err := json.Marshal(scoped)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal scoped session: %w", err)
	}
	return out, nil
}

// GetTranscriptPosition returns the number of messages in the transcript.
func (a *OpenCodeAgent) GetTranscriptPosition(path string) (int, error) {
	session, err := parseExportSessionFromFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if session == nil {
		return 0, nil
	}
	return len(session.Messages), nil
}

// ExtractModifiedFilesFromOffset extracts files modified by tool calls from the given message offset.
func (a *OpenCodeAgent) ExtractModifiedFilesFromOffset(path string, startOffset int) ([]string, int, error) {
	session, err := parseExportSessionFromFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	if session == nil {
		return nil, 0, nil
	}

	seen := make(map[string]bool)
	var files []string

	for i := startOffset; i < len(session.Messages); i++ {
		msg := session.Messages[i]
		if msg.Info.Role != roleAssistant {
			continue
		}
		for _, part := range msg.Parts {
			if part.Type != "tool" || part.State == nil {
				continue
			}
			if !slices.Contains(FileModificationTools, part.Tool) {
				continue
			}
			filePath := extractFilePathFromInput(part.State.Input)
			if filePath != "" && !seen[filePath] {
				seen[filePath] = true
				files = append(files, filePath)
			}
		}
	}

	return files, len(session.Messages), nil
}

// ExtractModifiedFiles extracts modified file paths from raw export JSON transcript bytes.
// This is the bytes-based equivalent of ExtractModifiedFilesFromOffset, used by ReadSession.
func ExtractModifiedFiles(data []byte) ([]string, error) {
	session, err := ParseExportSession(data)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, nil
	}

	seen := make(map[string]bool)
	var files []string

	for _, msg := range session.Messages {
		if msg.Info.Role != roleAssistant {
			continue
		}
		for _, part := range msg.Parts {
			if part.Type != "tool" || part.State == nil {
				continue
			}
			if !slices.Contains(FileModificationTools, part.Tool) {
				continue
			}
			filePath := extractFilePathFromInput(part.State.Input)
			if filePath != "" && !seen[filePath] {
				seen[filePath] = true
				files = append(files, filePath)
			}
		}
	}

	return files, nil
}

// extractFilePathFromInput extracts the file path from an OpenCode tool's input map.
// OpenCode uses camelCase keys (e.g., "filePath"), with "path" as a fallback.
func extractFilePathFromInput(input map[string]any) string {
	for _, key := range []string{"filePath", "path"} {
		if v, ok := input[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// ExtractPrompts extracts user prompt strings from the transcript starting at the given offset.
func (a *OpenCodeAgent) ExtractPrompts(sessionRef string, fromOffset int) ([]string, error) {
	session, err := parseExportSessionFromFile(sessionRef)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if session == nil {
		return nil, nil
	}

	var prompts []string
	for i := fromOffset; i < len(session.Messages); i++ {
		msg := session.Messages[i]
		if msg.Info.Role != roleUser {
			continue
		}
		// Extract text from parts
		content := ExtractTextFromParts(msg.Parts)
		if content != "" {
			prompts = append(prompts, content)
		}
	}

	return prompts, nil
}

// ExtractSummary extracts the last assistant message content as a summary.
func (a *OpenCodeAgent) ExtractSummary(sessionRef string) (string, error) {
	session, err := parseExportSessionFromFile(sessionRef)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if session == nil {
		return "", nil
	}

	for i := len(session.Messages) - 1; i >= 0; i-- {
		msg := session.Messages[i]
		if msg.Info.Role == roleAssistant {
			content := ExtractTextFromParts(msg.Parts)
			if content != "" {
				return content, nil
			}
		}
	}

	return "", nil
}

// ExtractTextFromParts extracts text content from message parts.
func ExtractTextFromParts(parts []Part) string {
	var texts []string
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// ExtractAllUserPrompts extracts all user prompts from raw export JSON transcript bytes.
// This is a package-level function used by the condensation path.
func ExtractAllUserPrompts(data []byte) ([]string, error) {
	session, err := ParseExportSession(data)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, nil
	}

	var prompts []string
	for _, msg := range session.Messages {
		if msg.Info.Role != roleUser {
			continue
		}
		content := ExtractTextFromParts(msg.Parts)
		if content != "" {
			prompts = append(prompts, content)
		}
	}
	return prompts, nil
}

// CalculateTokenUsageFromBytes computes token usage from raw export JSON transcript bytes
// starting at the given message offset.
// This is a package-level function used by the condensation path (which has bytes, not a file path).
func CalculateTokenUsageFromBytes(data []byte, startMessageIndex int) *agent.TokenUsage {
	session, err := ParseExportSession(data)
	if err != nil || session == nil {
		return &agent.TokenUsage{}
	}

	usage := &agent.TokenUsage{}
	for i := startMessageIndex; i < len(session.Messages); i++ {
		msg := session.Messages[i]
		if msg.Info.Role != roleAssistant || msg.Info.Tokens == nil {
			continue
		}
		usage.InputTokens += msg.Info.Tokens.Input
		usage.OutputTokens += msg.Info.Tokens.Output
		usage.CacheReadTokens += msg.Info.Tokens.Cache.Read
		usage.CacheCreationTokens += msg.Info.Tokens.Cache.Write
		usage.APICallCount++
	}

	return usage
}

// CalculateTokenUsage computes token usage from assistant messages starting at the given offset.
func (a *OpenCodeAgent) CalculateTokenUsage(sessionRef string, fromOffset int) (*agent.TokenUsage, error) {
	session, err := parseExportSessionFromFile(sessionRef)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil //nolint:nilnil // nil usage for nonexistent file is expected
		}
		return nil, fmt.Errorf("failed to parse transcript for token usage: %w", err)
	}
	if session == nil {
		return nil, nil //nolint:nilnil // nil usage for empty file is expected
	}

	usage := &agent.TokenUsage{}
	for i := fromOffset; i < len(session.Messages); i++ {
		msg := session.Messages[i]
		if msg.Info.Role != roleAssistant || msg.Info.Tokens == nil {
			continue
		}
		usage.InputTokens += msg.Info.Tokens.Input
		usage.OutputTokens += msg.Info.Tokens.Output
		usage.CacheReadTokens += msg.Info.Tokens.Cache.Read
		usage.CacheCreationTokens += msg.Info.Tokens.Cache.Write
		usage.APICallCount++
	}

	return usage, nil
}
