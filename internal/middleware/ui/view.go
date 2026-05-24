// Package ui implements UI-related middleware for GoClaw.
//
// This package contains middlewares that handle user interface concerns,
// including title generation and image viewing.
package ui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"goclaw/internal/middleware"
)

// ViewImageMiddleware handles image injection for multimodal models.
type ViewImageMiddleware struct {
	middleware.MiddlewareWrapper
}

// NewViewImageMiddleware constructs a ViewImageMiddleware.
func NewViewImageMiddleware() *ViewImageMiddleware {
	return &ViewImageMiddleware{}
}

// Name implements middleware.Middleware.
func (m *ViewImageMiddleware) Name() string { return "ViewImageMiddleware" }

// BeforeModel appends a new user message carrying all viewed images so
// that the next model call can see them, instead of mutating the
// existing human turn in-place.
func (m *ViewImageMiddleware) BeforeModel(_ context.Context, state *middleware.State) error {
	if state == nil || len(state.ViewedImages) == 0 {
		return nil
	}

	imageParts := make([]any, 0, len(state.ViewedImages))
	for path, imgData := range state.ViewedImages {
		if imgData.Base64 == "" {
			continue
		}

		mimeType := imgData.MIMEType
		if mimeType == "" {
			mimeType = guessMIMEType(path)
		}

		imageParts = append(imageParts, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": fmt.Sprintf("data:%s;base64,%s", mimeType, imgData.Base64),
			},
		})
	}

	if len(imageParts) == 0 {
		return nil
	}

	state.Messages = append(state.Messages, map[string]any{
		"role":    "user",
		"content": imageParts,
	})

	state.ViewedImages = nil

	return nil
}

// WrapToolCall intercepts view_image tool results and moves the image data into
// state.ViewedImages so that BeforeModel can inject it into the next model call.
// It also replaces the tool output with a short success message to avoid
// bloating the tool message with large base64 payloads.
func (m *ViewImageMiddleware) WrapToolCall(ctx context.Context, state *middleware.State, toolCall *middleware.ToolCall, handler middleware.ToolHandler) (*middleware.ToolResult, error) {
	result, err := handler(ctx, toolCall)
	if err != nil || result == nil || result.Error != nil || toolCall == nil || toolCall.Name != "view_image" {
		return result, err
	}

	img, ok := parseViewImageToolOutput(result.Output)
	if !ok || img.Base64 == "" {
		return result, nil
	}

	if state == nil {
		return result, nil
	}
	if state.ViewedImages == nil {
		state.ViewedImages = make(map[string]middleware.ViewedImage)
	}
	state.ViewedImages[img.Key] = middleware.ViewedImage{
		Base64:   img.Base64,
		MIMEType: img.MIMEType,
	}

	return &middleware.ToolResult{
		ID:     result.ID,
		Output: "Successfully read image",
	}, nil
}

// AfterModel is a no-op.
func (m *ViewImageMiddleware) AfterModel(_ context.Context, _ *middleware.State, _ *middleware.Response) error {
	return nil
}

type parsedViewImage struct {
	Key      string
	Base64   string
	MIMEType string
}

func parseViewImageToolOutput(output any) (parsedViewImage, bool) {
	if output == nil {
		return parsedViewImage{}, false
	}

	var raw map[string]any
	switch v := output.(type) {
	case map[string]any:
		raw = v
	case string:
		if err := json.Unmarshal([]byte(v), &raw); err != nil {
			return parsedViewImage{}, false
		}
	default:
		return parsedViewImage{}, false
	}

	base64Data, _ := raw["data"].(string)
	if base64Data == "" {
		base64Data, _ = raw["base64"].(string)
	}
	if base64Data == "" {
		return parsedViewImage{}, false
	}

	mimeType, _ := raw["mime"].(string)
	if mimeType == "" {
		mimeType, _ = raw["mime_type"].(string)
	}

	key, _ := raw["path"].(string)
	if key == "" {
		key, _ = raw["url"].(string)
	}
	if key == "" {
		key = "viewed_image"
	}

	return parsedViewImage{
		Key:      key,
		Base64:   base64Data,
		MIMEType: mimeType,
	}, true
}

func guessMIMEType(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	case strings.HasSuffix(lower, ".svg"):
		return "image/svg+xml"
	default:
		return "image/png"
	}
}

// IsValidBase64Image checks if the string is valid base64 image data.
func IsValidBase64Image(data string) bool {
	if data == "" {
		return false
	}
	_, err := base64.StdEncoding.DecodeString(data)
	return err == nil
}

var _ middleware.Middleware = (*ViewImageMiddleware)(nil)
