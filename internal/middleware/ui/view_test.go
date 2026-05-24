package ui

import (
	"context"
	"fmt"
	"testing"

	"goclaw/internal/middleware"
)

func TestViewImageMiddleware_Name(t *testing.T) {
	mw := NewViewImageMiddleware()
	if mw.Name() != "ViewImageMiddleware" {
		t.Errorf("expected name ViewImageMiddleware, got %s", mw.Name())
	}
}

func TestViewImageMiddleware_AfterModel_NoOp(t *testing.T) {
	mw := NewViewImageMiddleware()
	state := &middleware.State{}
	if err := mw.AfterModel(context.Background(), state, nil); err != nil {
		t.Errorf("AfterModel should be no-op, got error: %v", err)
	}
}

func TestViewImageMiddleware_NoImages(t *testing.T) {
	mw := NewViewImageMiddleware()
	state := &middleware.State{
		Messages: []map[string]any{
			{"role": "human", "content": "hello"},
		},
	}

	if err := mw.BeforeModel(context.Background(), state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(state.Messages) != 1 {
		t.Error("messages should not be modified")
	}
}

func TestViewImageMiddleware_NilState(t *testing.T) {
	mw := NewViewImageMiddleware()
	if err := mw.BeforeModel(context.Background(), nil); err != nil {
		t.Errorf("expected nil error for nil state, got %v", err)
	}
}

func TestViewImageMiddleware_InjectsImages(t *testing.T) {
	mw := NewViewImageMiddleware()

	state := &middleware.State{
		Messages: []map[string]any{
			{"role": "human", "content": "Look at this image"},
			{"role": "assistant", "content": "Sure"},
		},
		ViewedImages: map[string]middleware.ViewedImage{
			"test.png": {Base64: "YWJjZGVm", MIMEType: "image/png"},
		},
	}

	if err := mw.BeforeModel(context.Background(), state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(state.Messages) != 3 {
		t.Fatalf("expected 3 messages (orig 2 + 1 image-only user), got %d", len(state.Messages))
	}

	// The original human message must remain a plain string.
	if got, _ := state.Messages[0]["content"].(string); got != "Look at this image" {
		t.Errorf("original human message must not be mutated, got %v", state.Messages[0]["content"])
	}

	injected := state.Messages[2]
	if role, _ := injected["role"].(string); role != "user" {
		t.Errorf("expected injected message role=user, got %v", injected["role"])
	}

	parts, ok := injected["content"].([]any)
	if !ok {
		t.Fatalf("expected injected content to be []any, got %T", injected["content"])
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 image part in injected message, got %d", len(parts))
	}

	imagePart, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected image part to be map, got %T", parts[0])
	}
	if imagePart["type"] != "image_url" {
		t.Errorf("expected part type=image_url, got %v", imagePart["type"])
	}
	url := imagePart["image_url"].(map[string]any)["url"]
	if url != "data:image/png;base64,YWJjZGVm" {
		t.Errorf("unexpected image url: %v", url)
	}
}

func TestViewImageMiddleware_InjectsAfterLastMessage(t *testing.T) {
	mw := NewViewImageMiddleware()

	state := &middleware.State{
		Messages: []map[string]any{
			{"role": "human", "content": "first message"},
			{"role": "assistant", "content": "response"},
			{"role": "human", "content": "last message"},
		},
		ViewedImages: map[string]middleware.ViewedImage{
			"test.png": {Base64: "YWJj", MIMEType: "image/png"},
		},
	}

	if err := mw.BeforeModel(context.Background(), state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(state.Messages) != 4 {
		t.Fatalf("expected 4 messages after injection, got %d", len(state.Messages))
	}

	// All original messages should remain untouched.
	for i, want := range []string{"first message", "response", "last message"} {
		if got, _ := state.Messages[i]["content"].(string); got != want {
			t.Errorf("message[%d] mutated: got %v, want %q", i, state.Messages[i]["content"], want)
		}
	}

	injected := state.Messages[3]
	if role, _ := injected["role"].(string); role != "user" {
		t.Errorf("expected role=user, got %v", injected["role"])
	}
	if _, ok := injected["content"].([]any); !ok {
		t.Errorf("expected injected content to be []any, got %T", injected["content"])
	}
}

func TestViewImageMiddleware_SkipsEmptyBase64(t *testing.T) {
	mw := NewViewImageMiddleware()

	state := &middleware.State{
		Messages: []map[string]any{
			{"role": "human", "content": "hello"},
		},
		ViewedImages: map[string]middleware.ViewedImage{
			"empty.png": {Base64: "", MIMEType: "image/png"},
		},
	}

	mw.BeforeModel(context.Background(), state)

	// No new message should be appended when there is nothing to inject,
	// and the original human message must remain unchanged.
	if len(state.Messages) != 1 {
		t.Errorf("expected no new message to be appended, got %d", len(state.Messages))
	}
	if got, _ := state.Messages[0]["content"].(string); got != "hello" {
		t.Errorf("original human message must not be mutated, got %v", state.Messages[0]["content"])
	}
}

func TestViewImageMiddleware_ClearsViewedImages(t *testing.T) {
	mw := NewViewImageMiddleware()

	state := &middleware.State{
		Messages: []map[string]any{
			{"role": "human", "content": "hello"},
		},
		ViewedImages: map[string]middleware.ViewedImage{
			"test.png": {Base64: "YWJj", MIMEType: "image/png"},
		},
	}

	mw.BeforeModel(context.Background(), state)

	if state.ViewedImages != nil {
		t.Errorf("expected ViewedImages to be cleared, got %v", state.ViewedImages)
	}
}

func TestViewImageMiddleware_AppendsToExistingMultimodal(t *testing.T) {
	mw := NewViewImageMiddleware()

	original := []any{
		map[string]any{"type": "text", "text": "hello"},
	}
	state := &middleware.State{
		Messages: []map[string]any{
			{
				"role":    "human",
				"content": original,
			},
		},
		ViewedImages: map[string]middleware.ViewedImage{
			"test.png": {Base64: "YWJj", MIMEType: "image/png"},
		},
	}

	mw.BeforeModel(context.Background(), state)

	// The original multimodal user message must remain untouched.
	if got, _ := state.Messages[0]["content"].([]any); len(got) != 1 {
		t.Errorf("expected original multimodal content to be untouched, got %v", got)
	}
	if len(state.Messages) != 2 {
		t.Fatalf("expected a new image-only user message to be appended, got %d messages", len(state.Messages))
	}

	parts, ok := state.Messages[1]["content"].([]any)
	if !ok {
		t.Fatalf("expected appended content to be []any, got %T", state.Messages[1]["content"])
	}
	if len(parts) != 1 {
		t.Errorf("expected 1 image part in appended message, got %d", len(parts))
	}
}

func TestViewImageMiddleware_MultipleImages(t *testing.T) {
	mw := NewViewImageMiddleware()

	state := &middleware.State{
		Messages: []map[string]any{
			{"role": "human", "content": "check these"},
		},
		ViewedImages: map[string]middleware.ViewedImage{
			"a.png": {Base64: "YWJj", MIMEType: "image/png"},
			"b.jpg": {Base64: "ZGVm", MIMEType: "image/jpeg"},
		},
	}

	mw.BeforeModel(context.Background(), state)

	if len(state.Messages) != 2 {
		t.Fatalf("expected 2 messages (orig + injected), got %d", len(state.Messages))
	}

	parts, ok := state.Messages[1]["content"].([]any)
	if !ok {
		t.Fatalf("expected array content, got %T", state.Messages[1]["content"])
	}
	// Two images, no text in the injected user message.
	if len(parts) != 2 {
		t.Errorf("expected 2 image parts, got %d", len(parts))
	}
}

func TestGuessMIMEType(t *testing.T) {
	tests := []struct {
		path, want string
	}{
		{"test.png", "image/png"},
		{"test.PNG", "image/png"},
		{"test.jpg", "image/jpeg"},
		{"test.jpeg", "image/jpeg"},
		{"test.JPG", "image/jpeg"},
		{"test.gif", "image/gif"},
		{"test.webp", "image/webp"},
		{"test.svg", "image/svg+xml"},
		{"test.unknown", "image/png"}, // default
		{"", "image/png"},             // default
	}

	for _, tt := range tests {
		got := guessMIMEType(tt.path)
		if got != tt.want {
			t.Errorf("guessMIMEType(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestIsValidBase64Image(t *testing.T) {
	tests := []struct {
		data  string
		valid bool
	}{
		{"", false},
		{"aGVsbG8=", true}, // valid base64
		{"YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo=", true}, // valid
		{"!invalid", false},                            // invalid chars
		{"YWJj ZGVm", false},                           // space not valid
	}

	for _, tt := range tests {
		got := IsValidBase64Image(tt.data)
		if got != tt.valid {
			t.Errorf("IsValidBase64Image(%q) = %v, want %v", tt.data, got, tt.valid)
		}
	}
}

func TestViewImageMiddleware_WrapToolCall_InterceptsViewImage(t *testing.T) {
	mw := NewViewImageMiddleware()
	state := &middleware.State{}
	toolCall := &middleware.ToolCall{
		ID:   "call_1",
		Name: "view_image",
		Input: map[string]any{
			"path": "https://example.com/image.png",
		},
	}
	handler := func(_ context.Context, _ *middleware.ToolCall) (*middleware.ToolResult, error) {
		return &middleware.ToolResult{
			ID: "call_1",
			Output: map[string]any{
				"type": "image",
				"data": "YWJjZGVm",
				"mime": "image/png",
				"path": "https://example.com/image.png",
			},
		}, nil
	}

	result, err := mw.WrapToolCall(context.Background(), state, toolCall, handler)
	if err != nil {
		t.Fatalf("WrapToolCall returned error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Output != "Successfully read image" {
		t.Errorf("expected output 'Successfully read image', got %v", result.Output)
	}
	if state.ViewedImages == nil {
		t.Fatal("expected ViewedImages to be initialized")
	}
	img, ok := state.ViewedImages["https://example.com/image.png"]
	if !ok {
		t.Fatal("expected image to be stored in ViewedImages")
	}
	if img.Base64 != "YWJjZGVm" {
		t.Errorf("unexpected base64: %v", img.Base64)
	}
	if img.MIMEType != "image/png" {
		t.Errorf("unexpected mime: %v", img.MIMEType)
	}
}

func TestViewImageMiddleware_WrapToolCall_PassesThroughNonViewImage(t *testing.T) {
	mw := NewViewImageMiddleware()
	state := &middleware.State{}
	toolCall := &middleware.ToolCall{
		ID:   "call_2",
		Name: "read_file",
	}
	handler := func(_ context.Context, _ *middleware.ToolCall) (*middleware.ToolResult, error) {
		return &middleware.ToolResult{
			ID:     "call_2",
			Output: "file content",
		}, nil
	}

	result, err := mw.WrapToolCall(context.Background(), state, toolCall, handler)
	if err != nil {
		t.Fatalf("WrapToolCall returned error: %v", err)
	}
	if result == nil || result.Output != "file content" {
		t.Errorf("expected output to pass through, got %v", result)
	}
	if len(state.ViewedImages) != 0 {
		t.Errorf("expected no ViewedImages for non-view_image tool, got %v", state.ViewedImages)
	}
}

func TestViewImageMiddleware_WrapToolCall_PassesThroughViewImageError(t *testing.T) {
	mw := NewViewImageMiddleware()
	state := &middleware.State{}
	toolCall := &middleware.ToolCall{
		ID:   "call_3",
		Name: "view_image",
	}
	handler := func(_ context.Context, _ *middleware.ToolCall) (*middleware.ToolResult, error) {
		return &middleware.ToolResult{
			ID:    "call_3",
			Error: fmt.Errorf("read failed"),
		}, nil
	}

	result, err := mw.WrapToolCall(context.Background(), state, toolCall, handler)
	if err != nil {
		t.Fatalf("WrapToolCall returned error: %v", err)
	}
	if result == nil || result.Error == nil {
		t.Errorf("expected error result to pass through, got %v", result)
	}
	if len(state.ViewedImages) != 0 {
		t.Errorf("expected no ViewedImages on error, got %v", state.ViewedImages)
	}
}

func TestViewImageMiddleware_WrapToolCall_AcceptsStringOutput(t *testing.T) {
	mw := NewViewImageMiddleware()
	state := &middleware.State{}
	toolCall := &middleware.ToolCall{
		ID:   "call_4",
		Name: "view_image",
	}
	handler := func(_ context.Context, _ *middleware.ToolCall) (*middleware.ToolResult, error) {
		return &middleware.ToolResult{
			ID:     "call_4",
			Output: `{"type":"image","data":"dGVzdA==","mime_type":"image/jpeg","url":"https://example.com/photo.jpg"}`,
		}, nil
	}

	result, err := mw.WrapToolCall(context.Background(), state, toolCall, handler)
	if err != nil {
		t.Fatalf("WrapToolCall returned error: %v", err)
	}
	if result == nil || result.Output != "Successfully read image" {
		t.Errorf("expected success message, got %v", result)
	}
	img, ok := state.ViewedImages["https://example.com/photo.jpg"]
	if !ok {
		t.Fatal("expected image stored by url key")
	}
	if img.Base64 != "dGVzdA==" || img.MIMEType != "image/jpeg" {
		t.Errorf("unexpected image: %+v", img)
	}
}
