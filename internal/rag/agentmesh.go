package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AgentMeshAnswerClient invokes the bounded /v1/rag/answers contract after
// local retrieval. It never passes tenant identity in the request: AgentMesh
// derives it from the explicit API key.
type AgentMeshAnswerClient struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

type AgentMeshAnswerConfig struct {
	BaseURL        string
	APIKey         string
	Model          string
	TimeoutSeconds int
	Client         *http.Client
}

type GroundedCitation struct {
	SourceURI  string `json:"source_uri"`
	DocumentID string `json:"document_id"`
	Version    string `json:"version"`
	ChunkID    string `json:"chunk_id"`
	Hash       string `json:"hash"`
}

type GroundedFact struct {
	Text      string             `json:"text"`
	Citations []GroundedCitation `json:"citations"`
}

type GroundedAnswer struct {
	Context     AnswerContext  `json:"answer_context"`
	Facts       []GroundedFact `json:"facts,omitempty"`
	RefusalCode string         `json:"refusal_code,omitempty"`
	TraceID     string         `json:"trace_id,omitempty"`
	Provider    string         `json:"provider,omitempty"`
	Model       string         `json:"model,omitempty"`
}

func NewAgentMeshAnswerClient(cfg AgentMeshAnswerConfig) (*AgentMeshAnswerClient, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.Model) == "" {
		return nil, ErrProviderRefused
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: invalid AgentMesh base URL", ErrInvalidInput)
	}
	timeout := 60 * time.Second
	if cfg.TimeoutSeconds != 0 {
		if cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 300 {
			return nil, fmt.Errorf("%w: invalid AgentMesh answer timeout", ErrInvalidInput)
		}
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &AgentMeshAnswerClient{baseURL: strings.TrimRight(cfg.BaseURL, "/"), apiKey: cfg.APIKey, model: cfg.Model, client: client}, nil
}

func (c *AgentMeshAnswerClient) Answer(ctx context.Context, answer AnswerContext) (GroundedAnswer, error) {
	result := GroundedAnswer{Context: answer}
	if !answer.HasEvidence || len(answer.Items) == 0 {
		result.RefusalCode = "insufficient_evidence"
		return result, ErrNoEvidence
	}
	contexts, err := agentMeshContexts(answer)
	if err != nil {
		return result, err
	}
	payload := struct {
		Model    string             `json:"model"`
		Question string             `json:"question"`
		Contexts []agentMeshContext `json:"contexts"`
	}{Model: c.model, Question: answer.Question, Contexts: contexts}
	raw, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/rag/answers", bytes.NewReader(raw))
	if err != nil {
		return result, fmt.Errorf("create AgentMesh answer request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return result, fmt.Errorf("%w: AgentMesh answer request: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, fmt.Errorf("%w: AgentMesh answer returned HTTP %s", ErrUnavailable, resp.Status)
	}
	var decoded struct {
		Model       string         `json:"model"`
		Facts       []GroundedFact `json:"facts"`
		RefusalCode string         `json:"refusal_code"`
		TraceID     string         `json:"trace_id"`
		Provider    string         `json:"provider"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return result, fmt.Errorf("%w: decode AgentMesh answer: %v", ErrUnavailable, err)
	}
	result.Facts, result.RefusalCode, result.TraceID, result.Provider, result.Model = decoded.Facts, decoded.RefusalCode, decoded.TraceID, decoded.Provider, decoded.Model
	if err := validateGroundedResponse(decoded, contexts); err != nil {
		return result, err
	}
	return result, nil
}

// QueryAndAnswer is the production query path: local embedding/Qdrant
// retrieval produces an AnswerContext, then only its hash-bound chunks go to
// AgentMesh. SQL/DDL and no-evidence refusals never invoke AgentMesh.
func QueryAndAnswer(ctx context.Context, service *Service, answerer *AgentMeshAnswerClient, scope Scope, question string, options SearchOptions) (GroundedAnswer, error) {
	if service == nil || answerer == nil {
		return GroundedAnswer{}, fmt.Errorf("%w: retrieval service and answer client are required", ErrInvalidInput)
	}
	answer, err := service.Query(ctx, scope, question, options)
	if err != nil {
		if err == ErrNoEvidence {
			return GroundedAnswer{Context: answer, RefusalCode: "insufficient_evidence"}, err
		}
		return GroundedAnswer{Context: answer}, err
	}
	return answerer.Answer(ctx, answer)
}

type agentMeshContext struct {
	GroundedCitation
	Content string `json:"content"`
}

func agentMeshContexts(answer AnswerContext) ([]agentMeshContext, error) {
	if len(answer.Items) > 16 {
		return nil, fmt.Errorf("%w: too many contexts for AgentMesh", ErrInvalidInput)
	}
	contexts := make([]agentMeshContext, 0, len(answer.Items))
	for _, item := range answer.Items {
		citation := GroundedCitation{SourceURI: item.Citation.SourceURI, DocumentID: item.Citation.DocumentID, Version: item.Citation.Version, ChunkID: item.Citation.ChunkID, Hash: item.Citation.SHA256}
		if !item.Untrusted || strings.TrimSpace(item.Content) == "" || !validGroundedCitation(citation) || sha256Hex(item.Content) != citation.Hash {
			return nil, fmt.Errorf("%w: retrieved context hash/citation mismatch", ErrUnavailable)
		}
		contexts = append(contexts, agentMeshContext{GroundedCitation: citation, Content: item.Content})
	}
	return contexts, nil
}

func validateGroundedResponse(response struct {
	Model       string         `json:"model"`
	Facts       []GroundedFact `json:"facts"`
	RefusalCode string         `json:"refusal_code"`
	TraceID     string         `json:"trace_id"`
	Provider    string         `json:"provider"`
}, contexts []agentMeshContext) error {
	if response.RefusalCode != "" {
		if response.RefusalCode != "insufficient_evidence" || len(response.Facts) != 0 {
			return fmt.Errorf("%w: invalid AgentMesh refusal", ErrUnavailable)
		}
		return nil
	}
	if len(response.Facts) == 0 {
		return fmt.Errorf("%w: AgentMesh returned no grounded facts", ErrUnavailable)
	}
	allowed := make(map[GroundedCitation]struct{}, len(contexts))
	for _, context := range contexts {
		allowed[context.GroundedCitation] = struct{}{}
	}
	for _, fact := range response.Facts {
		if strings.TrimSpace(fact.Text) == "" || len(fact.Citations) == 0 {
			return fmt.Errorf("%w: invalid AgentMesh fact", ErrUnavailable)
		}
		for _, citation := range fact.Citations {
			if !validGroundedCitation(citation) {
				return fmt.Errorf("%w: incomplete AgentMesh citation", ErrCitationMismatch)
			}
			if _, ok := allowed[citation]; !ok {
				return ErrCitationMismatch
			}
		}
	}
	return nil
}

func validGroundedCitation(citation GroundedCitation) bool {
	return strings.TrimSpace(citation.SourceURI) != "" && strings.TrimSpace(citation.DocumentID) != "" && strings.TrimSpace(citation.Version) != "" && strings.TrimSpace(citation.ChunkID) != "" && len(citation.Hash) == 64
}
