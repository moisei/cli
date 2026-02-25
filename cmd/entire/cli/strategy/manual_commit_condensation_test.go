package strategy

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestGenerateContextFromPrompts_CJKTruncation(t *testing.T) {
	t.Parallel()

	// 600 CJK characters exceeds the 500-rune truncation limit.
	prompt := strings.Repeat("あ", 600)

	result := generateContextFromPrompts([]string{prompt})

	if !utf8.Valid(result) {
		t.Error("generateContextFromPrompts produced invalid UTF-8 when truncating a CJK prompt")
	}

	resultStr := string(result)
	if !strings.Contains(resultStr, "...") {
		t.Error("expected truncated CJK prompt to contain '...' suffix")
	}
	// Should not contain more than 500 CJK characters
	if strings.Contains(resultStr, strings.Repeat("あ", 501)) {
		t.Error("CJK prompt was not truncated")
	}
}

func TestGenerateContextFromPrompts_EmojiTruncation(t *testing.T) {
	t.Parallel()

	// 600 emoji exceeds the 500-rune truncation limit.
	prompt := strings.Repeat("🎉", 600)

	result := generateContextFromPrompts([]string{prompt})

	if !utf8.Valid(result) {
		t.Error("generateContextFromPrompts produced invalid UTF-8 when truncating an emoji prompt")
	}

	resultStr := string(result)
	if !strings.Contains(resultStr, "...") {
		t.Error("expected truncated emoji prompt to contain '...' suffix")
	}
}

func TestGenerateContextFromPrompts_ASCIITruncation(t *testing.T) {
	t.Parallel()

	// Pure ASCII: should truncate at 500 runes with "..." suffix.
	prompt := strings.Repeat("a", 600)

	result := generateContextFromPrompts([]string{prompt})

	if !utf8.Valid(result) {
		t.Error("generateContextFromPrompts produced invalid UTF-8 when truncating an ASCII prompt")
	}

	resultStr := string(result)
	if !strings.Contains(resultStr, "...") {
		t.Error("expected truncated prompt to contain '...' suffix")
	}

	if strings.Contains(resultStr, strings.Repeat("a", 501)) {
		t.Error("prompt was not truncated")
	}
}

func TestGenerateContextFromPrompts_ShortCJKNotTruncated(t *testing.T) {
	t.Parallel()

	// 200 CJK characters is under the 500-rune limit, should not be truncated.
	prompt := strings.Repeat("あ", 200)

	result := generateContextFromPrompts([]string{prompt})

	if !utf8.Valid(result) {
		t.Error("generateContextFromPrompts produced invalid UTF-8")
	}

	resultStr := string(result)
	if strings.Contains(resultStr, "...") {
		t.Error("short CJK prompt should not be truncated")
	}
}
