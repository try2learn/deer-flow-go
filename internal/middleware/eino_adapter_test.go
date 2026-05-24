package middleware

import (
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestApplyMiddlewareStateToAdk_PreservesToolCallMetadata(t *testing.T) {
	state := &State{
		Messages: []map[string]any{
			{
				"role":    "assistant",
				"content": "I'll read that file.",
				"tool_calls": []map[string]any{
					{
						"id":        "call_1",
						"name":      "read_file",
						"arguments": `{"path":"README.md"}`,
					},
				},
			},
			{
				"role":         "tool",
				"content":      "file content",
				"tool_call_id": "call_1",
				"tool_name":    "read_file",
			},
		},
	}
	adkState := &adk.ChatModelAgentState{}

	applyMiddlewareStateToAdk(state, adkState)

	if len(adkState.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(adkState.Messages))
	}

	assistant := adkState.Messages[0]
	if assistant.Role != schema.Assistant {
		t.Fatalf("expected assistant role, got %s", assistant.Role)
	}
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(assistant.ToolCalls))
	}
	if assistant.ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected tool call id call_1, got %q", assistant.ToolCalls[0].ID)
	}
	if assistant.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("expected tool name read_file, got %q", assistant.ToolCalls[0].Function.Name)
	}

	tool := adkState.Messages[1]
	if tool.Role != schema.Tool {
		t.Fatalf("expected tool role, got %s", tool.Role)
	}
	if tool.ToolCallID != "call_1" {
		t.Fatalf("expected tool_call_id call_1, got %q", tool.ToolCallID)
	}
	if tool.ToolName != "read_file" {
		t.Fatalf("expected tool_name read_file, got %q", tool.ToolName)
	}
}

func TestMessageToMap_PreservesToolMessageMetadata(t *testing.T) {
	msg := schema.ToolMessage("file content", "call_1", schema.WithToolName("read_file"))

	got := messageToMap(msg)

	if got["tool_call_id"] != "call_1" {
		t.Fatalf("expected tool_call_id call_1, got %v", got["tool_call_id"])
	}
	if got["tool_name"] != "read_file" {
		t.Fatalf("expected tool_name read_file, got %v", got["tool_name"])
	}
}

func TestApplyMiddlewareStateToAdk_PreservesUserMultimodalContent(t *testing.T) {
	state := &State{
		Messages: []map[string]any{
			{
				"role": "human",
				"content": []any{
					map[string]any{"type": "text", "text": "请解释这张图"},
					map[string]any{
						"type": "image_url",
						"image_url": map[string]any{
							"url": "data:image/png;base64,YWJj",
						},
					},
				},
			},
		},
	}
	adkState := &adk.ChatModelAgentState{}

	applyMiddlewareStateToAdk(state, adkState)

	if len(adkState.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(adkState.Messages))
	}
	msg := adkState.Messages[0]
	if msg.Role != schema.User {
		t.Fatalf("expected user role, got %s", msg.Role)
	}
	if msg.Content != "" {
		t.Fatalf("expected text content to be empty for multimodal message, got %q", msg.Content)
	}
	if len(msg.UserInputMultiContent) != 2 {
		t.Fatalf("expected 2 multimodal parts, got %d", len(msg.UserInputMultiContent))
	}
	if msg.UserInputMultiContent[0].Type != schema.ChatMessagePartTypeText || msg.UserInputMultiContent[0].Text != "请解释这张图" {
		t.Fatalf("unexpected text part: %+v", msg.UserInputMultiContent[0])
	}
	image := msg.UserInputMultiContent[1].Image
	if image == nil || image.Base64Data == nil || *image.Base64Data != "YWJj" {
		t.Fatalf("expected image base64 part, got %+v", msg.UserInputMultiContent[1])
	}
	if image.MIMEType != "image/png" {
		t.Fatalf("expected image/png, got %q", image.MIMEType)
	}
}

func TestMessageToMap_PreservesUserMultimodalContentRoundTrip(t *testing.T) {
	url := "https://example.com/photo.jpg"
	base64Data := "YWJj"
	original := &schema.Message{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "请解释这张图"},
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{URL: &url},
				},
			},
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						MIMEType:   "image/png",
						Base64Data: &base64Data,
					},
				},
			},
		},
	}

	mapped := messageToMap(original)

	parts, ok := mapped["content"].([]map[string]any)
	if !ok {
		t.Fatalf("expected content to be []map[string]any, got %T", mapped["content"])
	}
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts in mapped content, got %d", len(parts))
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "请解释这张图" {
		t.Fatalf("unexpected mapped text part: %+v", parts[0])
	}
	if parts[1]["type"] != "image_url" {
		t.Fatalf("expected image_url type, got %v", parts[1]["type"])
	}
	if u := parts[1]["image_url"].(map[string]any)["url"]; u != url {
		t.Fatalf("expected url %q, got %v", url, u)
	}
	if u := parts[2]["image_url"].(map[string]any)["url"]; u != "data:image/png;base64,YWJj" {
		t.Fatalf("expected data url, got %v", u)
	}

	state := &State{Messages: []map[string]any{mapped}}
	adkState := &adk.ChatModelAgentState{}
	applyMiddlewareStateToAdk(state, adkState)

	if len(adkState.Messages) != 1 {
		t.Fatalf("expected 1 message after round trip, got %d", len(adkState.Messages))
	}
	rebuilt := adkState.Messages[0]
	if rebuilt.Role != schema.User {
		t.Fatalf("expected user role after round trip, got %s", rebuilt.Role)
	}
	if len(rebuilt.UserInputMultiContent) != 3 {
		t.Fatalf("expected 3 parts after round trip, got %d", len(rebuilt.UserInputMultiContent))
	}
	if rebuilt.UserInputMultiContent[0].Text != "请解释这张图" {
		t.Fatalf("unexpected rebuilt text: %q", rebuilt.UserInputMultiContent[0].Text)
	}
	urlPart := rebuilt.UserInputMultiContent[1].Image
	if urlPart == nil || urlPart.URL == nil || *urlPart.URL != url {
		t.Fatalf("expected url image part, got %+v", urlPart)
	}
	b64Part := rebuilt.UserInputMultiContent[2].Image
	if b64Part == nil || b64Part.Base64Data == nil || *b64Part.Base64Data != base64Data {
		t.Fatalf("expected base64 image part, got %+v", b64Part)
	}
	if b64Part.MIMEType != "image/png" {
		t.Fatalf("expected image/png mime, got %q", b64Part.MIMEType)
	}
}
