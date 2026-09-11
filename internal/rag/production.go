package rag

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ProductionConfig is deliberately opt-in. Loading it never silently falls
// back to fake embeddings or an in-memory store, which would make a failed
// deployment appear to have durable retrieval.
type ProductionConfig struct {
	Embedding HTTPEmbeddingConfig
	Qdrant    QdrantConfig
	AgentMesh AgentMeshAnswerConfig
}

// LoadProductionConfig reads only the explicitly named RAG environment keys.
// Secrets are consumed by adapters and are never included in reports/errors.
func LoadProductionConfig(lookup func(string) string) (ProductionConfig, error) {
	if lookup == nil {
		lookup = os.Getenv
	}
	dimension, err := strconv.Atoi(strings.TrimSpace(lookup("RAG_EMBEDDING_DIMENSION")))
	if err != nil || dimension < 1 || dimension > 16_000 {
		return ProductionConfig{}, ErrProviderRefused
	}
	answerTimeout := 60
	if configured := strings.TrimSpace(lookup("RAG_AGENTMESH_TIMEOUT_SECONDS")); configured != "" {
		parsed, err := strconv.Atoi(configured)
		if err != nil || parsed < 1 || parsed > 300 {
			return ProductionConfig{}, ErrProviderRefused
		}
		answerTimeout = parsed
	}
	config := ProductionConfig{
		Embedding: HTTPEmbeddingConfig{
			Endpoint:  strings.TrimSpace(lookup("RAG_EMBEDDING_ENDPOINT")),
			APIKey:    strings.TrimSpace(lookup("RAG_EMBEDDING_API_KEY")),
			Model:     strings.TrimSpace(lookup("RAG_EMBEDDING_MODEL")),
			Dimension: dimension,
		},
		Qdrant: QdrantConfig{
			Endpoint:   strings.TrimSpace(lookup("RAG_QDRANT_ENDPOINT")),
			APIKey:     strings.TrimSpace(lookup("RAG_QDRANT_API_KEY")),
			Collection: strings.TrimSpace(lookup("RAG_QDRANT_COLLECTION")),
			Dimension:  dimension,
		},
		AgentMesh: AgentMeshAnswerConfig{
			BaseURL:        strings.TrimSpace(lookup("RAG_AGENTMESH_BASE_URL")),
			APIKey:         strings.TrimSpace(lookup("RAG_AGENTMESH_API_KEY")),
			Model:          strings.TrimSpace(lookup("RAG_AGENTMESH_RAG_MODEL")),
			TimeoutSeconds: answerTimeout,
		},
	}
	if config.Embedding.Endpoint == "" || config.Embedding.APIKey == "" || config.Embedding.Model == "" || config.Qdrant.Endpoint == "" || config.Qdrant.Collection == "" {
		return ProductionConfig{}, ErrProviderRefused
	}
	return config, nil
}

// NewProductionQueryService builds the complete production route: explicit
// embeddings, persistent Qdrant retrieval, then authenticated AgentMesh
// grounded answers. No constructor installs an offline substitute.
func NewProductionQueryService(ctx context.Context, cfg ProductionConfig, chunkCfg ChunkConfig) (*Service, *AgentMeshAnswerClient, error) {
	// Validate the final hop before any Qdrant health network request, so a
	// partial query configuration is a true controlled refusal.
	answerer, err := NewAgentMeshAnswerClient(cfg.AgentMesh)
	if err != nil {
		return nil, nil, err
	}
	service, _, err := NewProductionService(ctx, cfg, chunkCfg)
	if err != nil {
		return nil, nil, err
	}
	return service, answerer, nil
}

// NewProductionService validates explicit endpoint configuration and creates
// the persistent Qdrant collection. Callers receive a controlled error when a
// configured dependency is down; no memory store is substituted.
func NewProductionService(ctx context.Context, cfg ProductionConfig, chunkCfg ChunkConfig) (*Service, *QdrantVectorStore, error) {
	embedding, err := NewHTTPEmbeddingProvider(cfg.Embedding)
	if err != nil {
		return nil, nil, err
	}
	store, err := NewQdrantVectorStore(cfg.Qdrant)
	if err != nil {
		return nil, nil, err
	}
	if err := store.Health(ctx); err != nil {
		return nil, nil, fmt.Errorf("qdrant health: %w", err)
	}
	if err := store.EnsureCollection(ctx); err != nil {
		return nil, nil, fmt.Errorf("qdrant collection: %w", err)
	}
	service, err := NewService(embedding, store, chunkCfg)
	if err != nil {
		return nil, nil, err
	}
	return service, store, nil
}
