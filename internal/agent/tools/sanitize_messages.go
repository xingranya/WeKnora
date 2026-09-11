package tools

import (
	"html"

	"github.com/Tencent/WeKnora/internal/models/chat"
)

// SanitizeMessages validates and fixes a message array for LLM compatibility.
// It handles common issues that cause provider API errors:
//   - Ensures no consecutive same-role messages (some providers reject these)
//   - Verifies tool result messages have matching tool_call in the preceding assistant message
//   - Removes empty content messages that can cause API errors
//
// Returns the sanitized message slice (may be shorter than input).
func SanitizeMessages(messages []chat.Message) []chat.Message {
	if len(messages) == 0 {
		return messages
	}

	result := make([]chat.Message, 0, len(messages))
	for i, msg := range messages {
		// Skip empty non-system messages (some providers reject these)
		if msg.Content == "" && msg.Role != "system" &&
			msg.Role != "tool" && len(msg.ToolCalls) == 0 && len(msg.Images) == 0 {
			continue
		}

		// Verify tool result messages reference a valid tool call
		if msg.Role == "tool" && msg.ToolCallID != "" {
			if !hasMatchingToolCall(messages[:i], msg.ToolCallID) {
				// Preserve recoverable data without promoting external output to policy.
				msg.Role = "user"
				msg.Content = "<untrusted_tool_result name=\"" + html.EscapeString(msg.Name) +
					"\">\n" + html.EscapeString(msg.Content) + "\n</untrusted_tool_result>"
				msg.ToolCallID = ""
				msg.Name = ""
			}
		}

		// 孤立工具结果转换后也参与同角色合并，并保留用户追加的图片。
		if len(result) > 0 && msg.Role != "tool" {
			prev := result[len(result)-1]
			if prev.Role == msg.Role && prev.Role != "tool" {
				result[len(result)-1].Content += "\n\n" + msg.Content
				if len(msg.Images) > 0 {
					images := append([]string(nil), prev.Images...)
					result[len(result)-1].Images = append(images, msg.Images...)
				}
				continue
			}
		}

		result = append(result, msg)
	}

	return result
}

// hasMatchingToolCall checks if any preceding assistant message has a tool call with the given ID.
func hasMatchingToolCall(messages []chat.Message, toolCallID string) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				if tc.ID == toolCallID {
					return true
				}
			}
		}
	}
	return false
}
