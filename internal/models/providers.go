package models

import (
	"context"
	"github.com/cloudwego/eino-ext/components/model/ark"

	openai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"

	"goclaw/internal/config"
)

func init() {
	// 注册 OpenAI provider（兼容 OpenAI API 格式的服务，包括智谱 GLM 等）
	RegisterProviderBuilder("openai", buildOpenAIModel)

	RegisterProviderBuilder("ark", buildArkModel)
}

func buildArkModel(ctx context.Context, cfg config.ModelConfig, opts BuildOptions) (model.ToolCallingChatModel, error) {
	return ark.NewChatModel(ctx, &ark.ChatModelConfig{
		BaseURL: cfg.APIBase,
		APIKey:  cfg.APIKey,
		Model:   cfg.Model,
	})
}

func buildOpenAIModel(ctx context.Context, cfg config.ModelConfig, opts BuildOptions) (model.ToolCallingChatModel, error) {
	openaiCfg := &openai.ChatModelConfig{
		Model:   cfg.Model,
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
	}

	if cfg.MaxTokens > 0 {
		openaiCfg.MaxCompletionTokens = &cfg.MaxTokens
	}

	if cfg.Temperature != nil {
		t := float32(*cfg.Temperature)
		openaiCfg.Temperature = &t
	}

	return openai.NewChatModel(ctx, openaiCfg)
}
