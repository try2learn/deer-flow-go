# Debug Session: missing-tool-call-id

Status: [OPEN]

## Symptom

Running the GoClaw agent after using a tool fails with:

`agent/run_error: [NodeRunError] Error code: 400 - MissingParameter: missing messages.tool_call_id`

Node path:

`[node_1, ChatModel]`

## Hypotheses

1. A tool response message is created without copying the original tool call ID into `schema.Message.ToolCallID`.
2. A middleware mutates or reconstructs tool messages and drops `ToolCallID`.
3. The subagent `task` tool returns a result that is converted into a tool message through a path different from normal tools.
4. Message history persistence or checkpoint restore serializes/deserializes tool messages without `tool_call_id`.
5. The model provider requires OpenAI-compatible `tool_call_id`, while Eino/GoClaw is emitting a schema variant missing that field.

## Evidence Plan

- Inspect the tool execution and message conversion path.
- Instrument only message-shape diagnostics before ChatModel invocation if static inspection is insufficient.
- Confirm whether any `schema.Tool` message has empty `ToolCallID`.

## Static Evidence

- `internal/middleware/eino_adapter.go:32-39` calls `applyMiddlewareStateToAdk` after every `BeforeChatModel`.
- `internal/middleware/eino_adapter.go:255-271` converts `schema.Message` to map but does not preserve `ToolCallID`, `ToolName`, or `Name`.
- `internal/middleware/eino_adapter.go:276-306` reconstructs every `schema.Message` with only `Role` and `Content`, dropping `ToolCalls`, `ToolCallID`, and `ToolName`.
- `internal/agent/executor.go:458-508` has a safer conversion pair that does preserve `tool_calls`, `tool_call_id`, and `tool_name`, indicating the missing-field behavior is specific to the middleware adapter path.

## Current Assessment

Hypothesis 2 is strongly supported: middleware adapter reconstructs messages and drops tool-call metadata before the next ChatModel invocation.

## Fix Applied

- Updated `internal/middleware/eino_adapter.go` to preserve `Name`, `ToolCalls`, `ToolCallID`, and `ToolName` during message map conversion.
- Updated `applyMiddlewareStateToAdk` to reconstruct assistant/tool messages through schema helpers instead of bare `{Role, Content}` messages.
- Added regression tests in `internal/middleware/eino_adapter_test.go`.

## Verification

- `gofmt -w internal/middleware/eino_adapter.go internal/middleware/eino_adapter_test.go`
- `GOTOOLCHAIN=local go test ./internal/middleware/...` passed.
- `GOTOOLCHAIN=local go test ./internal/agent/...` passed.
- IDE diagnostics for both edited files are clean.
