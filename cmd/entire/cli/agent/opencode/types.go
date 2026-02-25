package opencode

// sessionInfoRaw matches the JSON payload piped from the OpenCode plugin for session events.
// The plugin sends only session_id; Go calls `opencode export` to get the transcript.
type sessionInfoRaw struct {
	SessionID string `json:"session_id"`
}

// turnStartRaw matches the JSON payload for turn-start (user prompt submission).
type turnStartRaw struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
}

// --- Export JSON types (from `opencode export`) ---

// ExportSession represents the top-level structure of `opencode export` output.
// This is OpenCode's native format for session data.
type ExportSession struct {
	Info     SessionInfo     `json:"info"`
	Messages []ExportMessage `json:"messages"`
}

// SessionInfo contains session metadata from the export.
type SessionInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	CreatedAt int64  `json:"createdAt,omitempty"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
}

// ExportMessage represents a single message in the export format.
// Each message contains info (metadata) and parts (content).
type ExportMessage struct {
	Info  MessageInfo `json:"info"`
	Parts []Part      `json:"parts"`
}

// MessageInfo contains message metadata.
type MessageInfo struct {
	ID        string  `json:"id"`
	SessionID string  `json:"sessionID,omitempty"`
	Role      string  `json:"role"` // "user" or "assistant"
	Time      Time    `json:"time"`
	Tokens    *Tokens `json:"tokens,omitempty"`
	Cost      float64 `json:"cost,omitempty"`
}

// Message role constants.
const (
	roleAssistant = "assistant"
	roleUser      = "user"
)

// Time holds message timestamps.
type Time struct {
	Created   int64 `json:"created"`
	Completed int64 `json:"completed,omitempty"`
}

// Tokens holds token usage from assistant messages.
type Tokens struct {
	Input     int   `json:"input"`
	Output    int   `json:"output"`
	Reasoning int   `json:"reasoning"`
	Cache     Cache `json:"cache"`
}

// Cache holds cache-related token counts.
type Cache struct {
	Read  int `json:"read"`
	Write int `json:"write"`
}

// Part represents a message part (text, tool, etc.).
type Part struct {
	Type   string     `json:"type"` // "text", "tool", etc.
	Text   string     `json:"text,omitempty"`
	Tool   string     `json:"tool,omitempty"`
	CallID string     `json:"callID,omitempty"`
	State  *ToolState `json:"state,omitempty"`
}

// ToolState represents tool execution state.
type ToolState struct {
	Status string         `json:"status"` // "pending", "running", "completed", "error"
	Input  map[string]any `json:"input,omitempty"`
	Output string         `json:"output,omitempty"`
}

// FileModificationTools are tools in OpenCode that modify files on disk.
// These match the actual tool names from OpenCode's source:
//   - edit:  internal/llm/tools/edit.go  (EditToolName)
//   - write: internal/llm/tools/write.go (WriteToolName)
//   - patch: internal/llm/tools/patch.go (PatchToolName)
var FileModificationTools = []string{
	"edit",
	"write",
	"patch",
}
