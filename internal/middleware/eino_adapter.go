// Package middleware provides an adapter to convert Middleware to adk.AgentMiddleware.
package middleware

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"goclaw/internal/logging"
)

// AdaptMiddlewares converts a slice of Middleware to adk.AgentMiddleware.
// The new Eino API uses AgentMiddleware with BeforeChatModel/AfterChatModel/WrapToolCall hooks.
func AdaptMiddlewares(middlewares []Middleware) []adk.AgentMiddleware {
	result := make([]adk.AgentMiddleware, 0, len(middlewares))
	for _, mw := range middlewares {
		result = append(result, adaptMiddleware(mw))
	}
	return result
}

// middlewareStateSessionKey is the key used to store middleware state in session values.
// This must match the key used in executor.go.
const middlewareStateSessionKey = "__middleware_state__"

// adaptMiddleware converts a single Middleware to adk.AgentMiddleware.
func adaptMiddleware(mw Middleware) adk.AgentMiddleware {
	return adk.AgentMiddleware{
		BeforeChatModel: func(ctx context.Context, state *adk.ChatModelAgentState) error {
			mwState := getOrCreateMiddlewareState(ctx, state)
			if err := mw.BeforeModel(ctx, mwState); err != nil {
				return err
			}
			// Write back modified state to adk state and session values
			applyMiddlewareStateToAdk(mwState, state)
			saveMiddlewareStateToSession(ctx, mwState)
			return nil
		},
		AfterChatModel: func(ctx context.Context, state *adk.ChatModelAgentState) error {
			mwState := getOrCreateMiddlewareState(ctx, state)
			resp := toMiddlewareResponse(state)

			if err := mw.AfterModel(ctx, mwState, resp); err != nil {
				logging.Warn("middleware AfterModel error (non-fatal)",
					"middleware", mw.Name(),
					"error", err,
				)
			}
			// Save state back to session values (includes Title, etc.)
			saveMiddlewareStateToSession(ctx, mwState)
			return nil
		},
		WrapToolCall: composeToolMiddleware(mw),
	}
}

// getOrCreateMiddlewareState retrieves middleware state from session values or creates a new one.
func getOrCreateMiddlewareState(ctx context.Context, state *adk.ChatModelAgentState) *State {
	// Try to get existing state from session values
	vals := adk.GetSessionValues(ctx)
	if vals != nil {
		if cached, ok := vals[middlewareStateSessionKey].(*State); ok && cached != nil {
			// Update messages from current adk state
			cached.Messages = make([]map[string]any, 0, len(state.Messages))
			for _, msg := range state.Messages {
				cached.Messages = append(cached.Messages, messageToMap(msg))
			}
			return cached
		}
	}

	// Create new state
	mwState := toMiddlewareState(state)
	if vals != nil {
		// Try to get thread_id from session values
		if tid, ok := vals["thread_id"].(string); ok {
			mwState.ThreadID = tid
		}
	}
	return mwState
}

// saveMiddlewareStateToSession stores middleware state in session values.
func saveMiddlewareStateToSession(ctx context.Context, state *State) {
	if state == nil {
		return
	}
	adk.AddSessionValue(ctx, middlewareStateSessionKey, state)
}

// composeToolMiddleware creates a compose.ToolMiddleware from a single middleware.
func composeToolMiddleware(mw Middleware) compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				// Get state from context
				state := extractStateFromContext(ctx)
				if state == nil {
					state = &State{}
					saveMiddlewareStateToSession(ctx, state)
				}

				// Parse arguments JSON
				var args map[string]any
				if input.Arguments != "" {
					_ = json.Unmarshal([]byte(input.Arguments), &args)
				}
				if args == nil {
					args = map[string]any{}
				}

				// Create tool call representation
				toolCall := &ToolCall{
					ID:    input.CallID,
					Name:  input.Name,
					Input: args,
				}

				// Wrap handler
				handler := func(callCtx context.Context, call *ToolCall) (*ToolResult, error) {
					out, err := next(callCtx, input)
					if err != nil {
						return &ToolResult{ID: call.ID, Error: err}, nil
					}
					return &ToolResult{ID: call.ID, Output: out.Result}, nil
				}

				result, err := mw.WrapToolCall(ctx, state, toolCall, handler)
				if err != nil {
					return nil, err
				}
				if result.Error != nil {
					return nil, result.Error
				}
				saveMiddlewareStateToSession(ctx, state)

				return &compose.ToolOutput{Result: middlewareToolOutputToString(result.Output)}, nil
			}
		},
		Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
				// For streaming, we just pass through (middleware wrap is for sync calls)
				return next(ctx, input)
			}
		},
	}
}

// ComposeToolMiddleware creates a compose.ToolMiddleware from all middlewares.
func ComposeToolMiddleware(middlewares []Middleware) compose.ToolMiddleware {
	if len(middlewares) == 0 {
		return compose.ToolMiddleware{}
	}

	// Build chain from all WrapToolCall handlers
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			handler := func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				return next(ctx, input)
			}

			// Chain middlewares in reverse order
			for i := len(middlewares) - 1; i >= 0; i-- {
				mw := middlewares[i]
				innerHandler := handler
				handler = func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
					state := extractStateFromContext(ctx)
					if state == nil {
						state = &State{}
						saveMiddlewareStateToSession(ctx, state)
					}

					var args map[string]any
					if input.Arguments != "" {
						_ = json.Unmarshal([]byte(input.Arguments), &args)
					}
					if args == nil {
						args = map[string]any{}
					}

					toolCall := &ToolCall{
						ID:    input.CallID,
						Name:  input.Name,
						Input: args,
					}

					inner := func(callCtx context.Context, call *ToolCall) (*ToolResult, error) {
						out, err := innerHandler(callCtx, input)
						if err != nil {
							return &ToolResult{ID: call.ID, Error: err}, nil
						}
						return &ToolResult{ID: call.ID, Output: out.Result}, nil
					}

					result, err := mw.WrapToolCall(ctx, state, toolCall, inner)
					if err != nil {
						return nil, err
					}
					if result.Error != nil {
						return nil, result.Error
					}
					saveMiddlewareStateToSession(ctx, state)

					return &compose.ToolOutput{Result: middlewareToolOutputToString(result.Output)}, nil
				}
			}

			return handler
		},
	}
}

// Helper functions

func toMiddlewareState(adkState *adk.ChatModelAgentState) *State {
	state := &State{
		Messages: make([]map[string]any, 0, len(adkState.Messages)),
	}

	for _, msg := range adkState.Messages {
		state.Messages = append(state.Messages, messageToMap(msg))
	}

	return state
}

func toMiddlewareResponse(adkState *adk.ChatModelAgentState) *Response {
	resp := &Response{
		ToolCalls: make([]map[string]any, 0),
	}

	if len(adkState.Messages) > 0 {
		lastMsg := adkState.Messages[len(adkState.Messages)-1]
		if lastMsg.Role == schema.Assistant {
			resp.FinalMessage = lastMsg.Content
			if len(lastMsg.ToolCalls) > 0 {
				for _, tc := range lastMsg.ToolCalls {
					resp.ToolCalls = append(resp.ToolCalls, map[string]any{
						"id":       tc.ID,
						"name":     tc.Function.Name,
						"input":    tc.Function.Arguments,
						"response": "",
					})
				}
			}
		}
	}

	return resp
}

func messageToMap(msg *schema.Message) map[string]any {
	m := map[string]any{
		"role":    string(msg.Role),
		"content": msg.Content,
	}
	if parts := userInputPartsToMap(msg.UserInputMultiContent); len(parts) > 0 {
		m["content"] = parts
	}
	if msg.Name != "" {
		m["name"] = msg.Name
	}
	if len(msg.ToolCalls) > 0 {
		toolCalls := make([]map[string]any, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			toolCalls = append(toolCalls, map[string]any{
				"id":        tc.ID,
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
				"input":     tc.Function.Arguments,
			})
		}
		m["tool_calls"] = toolCalls
	}
	if msg.ToolCallID != "" {
		m["tool_call_id"] = msg.ToolCallID
	}
	if msg.ToolName != "" {
		m["tool_name"] = msg.ToolName
	}
	return m
}

// applyMiddlewareStateToAdk writes back modified middleware state to adk state.
// This is critical for memory injection which modifies Messages.
func applyMiddlewareStateToAdk(mwState *State, adkState *adk.ChatModelAgentState) {
	if mwState == nil || adkState == nil {
		return
	}

	// Convert modified messages back to adk state
	adkState.Messages = make([]*schema.Message, 0, len(mwState.Messages))
	for _, msg := range mwState.Messages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)

		switch role {
		case "system":
			adkState.Messages = append(adkState.Messages, schema.SystemMessage(content))
		case "user", "human":
			if parts := mapToUserInputParts(msg["content"]); len(parts) > 0 {
				adkState.Messages = append(adkState.Messages, &schema.Message{
					Role:                  schema.User,
					UserInputMultiContent: parts,
				})
			} else {
				adkState.Messages = append(adkState.Messages, schema.UserMessage(content))
			}
		case "assistant", "ai":
			toolCalls := mapToToolCalls(msg["tool_calls"])
			message := schema.AssistantMessage(content, toolCalls)
			if name, ok := msg["name"].(string); ok {
				message.Name = name
			}
			adkState.Messages = append(adkState.Messages, message)
		case "tool":
			toolCallID, _ := msg["tool_call_id"].(string)
			toolName, _ := msg["tool_name"].(string)
			opts := make([]schema.ToolMessageOption, 0, 1)
			if toolName != "" {
				opts = append(opts, schema.WithToolName(toolName))
			}

			adkState.Messages = append(adkState.Messages, schema.ToolMessage(content, toolCallID, opts...))
		default:
			adkState.Messages = append(adkState.Messages, schema.UserMessage(content))
		}
	}
}

func mapToToolCalls(raw any) []schema.ToolCall {
	toCall := func(v map[string]any) schema.ToolCall {
		id, _ := v["id"].(string)
		name, _ := v["name"].(string)
		args, _ := v["arguments"].(string)
		if args == "" {
			args, _ = v["input"].(string)
		}
		return schema.ToolCall{
			ID:       id,
			Type:     "function",
			Function: schema.FunctionCall{Name: name, Arguments: args},
		}
	}

	out := make([]schema.ToolCall, 0)
	switch vv := raw.(type) {
	case []map[string]any:
		for _, v := range vv {
			out = append(out, toCall(v))
		}
	case []any:
		for _, item := range vv {
			if v, ok := item.(map[string]any); ok {
				out = append(out, toCall(v))
			}
		}
	}
	return out
}

type middlewareStateKey struct{}

func extractStateFromContext(ctx context.Context) *State {
	if state, ok := ctx.Value(middlewareStateKey{}).(*State); ok {
		return state
	}
	if vals := adk.GetSessionValues(ctx); vals != nil {
		if state, ok := vals[middlewareStateSessionKey].(*State); ok {
			return state
		}
	}
	return nil
}

func userInputPartsToMap(parts []schema.MessageInputPart) []map[string]any {
	if len(parts) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			out = append(out, map[string]any{
				"type": "text",
				"text": part.Text,
			})
		case schema.ChatMessagePartTypeImageURL:
			if part.Image == nil {
				continue
			}
			url := ""
			if part.Image.URL != nil {
				url = *part.Image.URL
			} else if part.Image.Base64Data != nil {
				url = "data:" + part.Image.MIMEType + ";base64," + *part.Image.Base64Data
			}
			out = append(out, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": url,
				},
			})
		}
	}
	return out
}

func mapToUserInputParts(content any) []schema.MessageInputPart {
	var rawParts []any
	switch v := content.(type) {
	case []any:
		rawParts = v
	case []map[string]any:
		rawParts = make([]any, 0, len(v))
		for _, part := range v {
			rawParts = append(rawParts, part)
		}
	default:
		return nil
	}

	parts := make([]schema.MessageInputPart, 0, len(rawParts))
	for _, rawPart := range rawParts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}

		partType, _ := part["type"].(string)
		switch partType {
		case "text":
			text, _ := part["text"].(string)
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: text,
			})
		case "image_url":
			image := mapToMessageInputImage(part)
			if image != nil {
				parts = append(parts, schema.MessageInputPart{
					Type:  schema.ChatMessagePartTypeImageURL,
					Image: image,
				})
			}
		}
	}

	return parts
}

func mapToMessageInputImage(part map[string]any) *schema.MessageInputImage {
	rawImageURL, ok := part["image_url"].(map[string]any)
	if !ok {
		return nil
	}

	rawURL, _ := rawImageURL["url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}

	common := schema.MessagePartCommon{}
	if mimeType, data, ok := splitDataImageURL(rawURL); ok {
		common.MIMEType = mimeType
		common.Base64Data = &data
	} else {
		common.URL = &rawURL
	}

	return &schema.MessageInputImage{
		MessagePartCommon: common,
		Detail:            schema.ImageURLDetailAuto,
	}
}

func splitDataImageURL(rawURL string) (mimeType, data string, ok bool) {
	const marker = ";base64,"
	if !strings.HasPrefix(rawURL, "data:image/") {
		return "", "", false
	}
	idx := strings.Index(rawURL, marker)
	if idx < 0 {
		return "", "", false
	}
	mimeType = strings.TrimPrefix(rawURL[:idx], "data:")
	data = rawURL[idx+len(marker):]
	return mimeType, data, mimeType != "" && data != ""
}

func middlewareToolOutputToString(output any) string {
	if output == nil {
		return ""
	}
	if str, ok := output.(string); ok {
		return str
	}
	if bs, err := json.Marshal(output); err == nil {
		return string(bs)
	}
	return ""
}
