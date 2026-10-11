package agentgraph

import (
	"context"
	"errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"io"
	"net/http"
	"sqlsentinel/internal/telemetry"
	"strings"
	"time"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/callbacks"
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
	callback := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			ctx, _ = otel.Tracer("eino.callbacks").Start(ctx, "eino.graph.node")
			return ctx
		}).OnEndFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackOutput) context.Context {
		trace.SpanFromContext(ctx).End()
		return ctx
	}).OnErrorFn(func(ctx context.Context, _ *callbacks.RunInfo, _ error) context.Context {
		span := trace.SpanFromContext(ctx)
		span.SetStatus(codes.Error, "graph_failed")
		span.End()
		return ctx
	}).Build()
	return n.runnable.Invoke(ctx, prompt, compose.WithCallbacks(callback))
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
		HTTPClient: &http.Client{Timeout: 90 * time.Second, Transport: telemetry.Transport{}},
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
	ctx, span := otel.Tracer("eino").Start(ctx, "eino.draft")
	defer span.End()
	stream, err := n.model.Stream(ctx, []*schema.Message{schema.UserMessage(prompt)})
	if err != nil {
		return "", err
	}
	defer stream.Close()
	var output strings.Builder
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", errors.New("model_stream_failed")
		}
		if output.Len()+len(message.Content) > 65536 {
			return "", errors.New("model_output_too_large")
		}
		output.WriteString(message.Content)
	}
	if output.Len() == 0 {
		return "", errors.New("model_output_empty")
	}
	return output.String(), nil
}
