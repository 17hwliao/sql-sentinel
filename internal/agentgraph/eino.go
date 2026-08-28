package agentgraph

import (
	"context"
	"errors"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type einoGraphNode struct {
	runnable compose.Runnable[string, string]
}

// NewEinoGraph wraps exactly one draft node in an Eino Compose graph. The
// wrapped node remains injectable, so graph behavior is testable offline.
func NewEinoGraph(node DraftNode) (DraftNode, error) {
	if node == nil {
		return nil, errors.New("draft node is required")
	}
	chain := compose.NewChain[string, string]()
	chain.AppendLambda(compose.InvokableLambda(func(ctx context.Context, prompt string) (string, error) {
		return node.Draft(ctx, prompt)
	}))
	runnable, err := chain.Compile(context.Background())
	if err != nil {
		return nil, err
	}
	return einoGraphNode{runnable: runnable}, nil
}

func (n einoGraphNode) Draft(ctx context.Context, prompt string) (string, error) {
	return n.runnable.Invoke(ctx, prompt)
}

type openAINode struct {
	model *openaimodel.ChatModel
}

// NewOpenAINode creates the sole real-model adapter. Callers must provide
// credentials and the optional OpenAI-compatible endpoint explicitly; no
// environment lookup or network call happens here.
func NewOpenAINode(ctx context.Context, apiKey, modelName, baseURL string) (DraftNode, error) {
	if apiKey == "" || modelName == "" {
		return nil, errors.New("OpenAI API key and model are required")
	}
	temperature := float32(0)
	config := &openaimodel.ChatModelConfig{
		APIKey: apiKey, Model: modelName, Temperature: &temperature,
	}
	if baseURL != "" {
		config.BaseURL = baseURL
	}
	model, err := openaimodel.NewChatModel(ctx, config)
	if err != nil {
		return nil, err
	}
	return openAINode{model: model}, nil
}

func (n openAINode) Draft(ctx context.Context, prompt string) (string, error) {
	message, err := n.model.Generate(ctx, []*schema.Message{schema.UserMessage(prompt)})
	if err != nil {
		return "", err
	}
	return message.Content, nil
}
