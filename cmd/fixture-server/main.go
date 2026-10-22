// fixture-server is for isolated acceptance only. It makes no external calls.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var state struct {
	sync.Mutex
	Mode         string
	Calls        int
	Comments     []map[string]any
	Head         string
	MaxTokens    int
	Chunks       int
	DelayMS      int
	PayloadBytes int
	Active       int
	Finished     int
	Cancelled    int
}

const sampleSQL = "SELECT id, status, created_at\nFROM orders\nWHERE user_id = 42 AND status = 'paid'\nORDER BY created_at DESC LIMIT 20;"

func main() {
	state.Head = strings.Repeat("1", 40)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /models", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": "fixture"}}})
	})
	mux.HandleFunc("/chat/completions", chat)
	mux.HandleFunc("POST /api/embed", func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Input []string }
		json.NewDecoder(r.Body).Decode(&input)
		vectors := make([][]float64, len(input.Input))
		for i := range vectors {
			vectors[i] = []float64{0.1, 0.2, 0.3}
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
	})
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"models":[{"name":"fixture"}]}`) })
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"message":{"content":"fixture ollama response"},"done":true,"prompt_eval_count":10,"eval_count":5}`)
	})
	mux.HandleFunc("/control", control)
	mux.HandleFunc("/repos/", github)
	log.Fatal(http.ListenAndServe(":8090", mux))
}
func chat(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Stream    bool `json:"stream"`
		MaxTokens int  `json:"max_tokens"`
		Messages  []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&input) != nil || !input.Stream {
		http.Error(w, "stream required", 400)
		return
	}
	state.Lock()
	state.Calls++
	state.MaxTokens = input.MaxTokens
	mode := state.Mode
	chunks, delay, payloadBytes := state.Chunks, state.DelayMS, state.PayloadBytes
	state.Active++
	state.Unlock()
	completed := false
	defer func() {
		state.Lock()
		defer state.Unlock()
		state.Active--
		if completed {
			state.Finished++
		} else {
			state.Cancelled++
		}
	}()
	if mode == "unavailable" {
		http.Error(w, "fixture unavailable", 503)
		return
	}
	if mode == "delay" {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
	text := "fixture response"
	allMessages := ""
	for _, message := range input.Messages {
		allMessages += message.Content + "\n"
	}
	for _, message := range input.Messages {
		if strings.Contains(message.Content, "CandidateSpec") {
			text = `{"table":"orders","index_name":"idx_cand_model","columns":[{"name":"user_id","direction":"ASC"},{"name":"status","direction":"ASC"},{"name":"created_at","direction":"DESC"}]}`
		}
		if strings.Contains(message.Content, "citation_chunk_ids") {
			id := regexp.MustCompile(`"chunk_id":"([a-f0-9]{32})"`).FindStringSubmatch(message.Content)
			text = `{"answer":"","citation_chunk_ids":[]}`
			if len(id) == 2 {
				raw, _ := json.Marshal(map[string]any{"answer": "The project uses persistent queues and scoped evidence.", "citation_chunk_ids": []string{id[1]}})
				text = string(raw)
			}
		}
	}
	if strings.Contains(allMessages, "citation_chunk_ids") {
		id := regexp.MustCompile(`"chunk_id":"([a-f0-9]{32})"`).FindStringSubmatch(allMessages)
		text = `{"answer":"","citation_chunk_ids":[]}`
		if len(id) == 2 {
			raw, _ := json.Marshal(map[string]any{"answer": "The project uses persistent queues and scoped evidence.", "citation_chunk_ids": []string{id[1]}})
			text = string(raw)
		}
	}
	if mode == "overrun" {
		text = strings.Repeat("x", 5000)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	payload, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": text}}}})
	fmt.Fprintf(w, "data: %s\n\n", payload)
	w.(http.Flusher).Flush()
	if mode == "missing_usage" {
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	if mode == "long_stream" || mode == "paced_stream" {
		if chunks < 1 {
			chunks = 4000
		}
		for i := 0; i < chunks; i++ {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			if delay > 0 {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(time.Duration(delay) * time.Millisecond):
				}
			}
			if payloadBytes < 1 {
				payloadBytes = 1
			}
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": strings.Repeat("x", payloadBytes)}}}})
			if _, err := fmt.Fprintf(w, "data: %s\n\n", chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
		fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":%d}}\n\ndata: [DONE]\n\n", chunks+16)
		completed = true
		return
	}
	fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":50}}\n\ndata: [DONE]\n\n")
	completed = true
}
func control(w http.ResponseWriter, r *http.Request) {
	state.Lock()
	defer state.Unlock()
	if r.Method == "POST" {
		var in struct {
			Mode, Head                    string
			Chunks, DelayMS, PayloadBytes int
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil || in.Chunks < 0 || in.Chunks > 4000 || in.DelayMS < 0 || in.DelayMS > 250 || in.PayloadBytes < 0 || in.PayloadBytes > 8192 {
			http.Error(w, "invalid control", 400)
			return
		}
		state.Mode = in.Mode
		state.Chunks, state.DelayMS = in.Chunks, in.DelayMS
		state.PayloadBytes = in.PayloadBytes
		if in.Head != "" {
			state.Head = in.Head
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"mode": state.Mode, "calls": state.Calls, "max_tokens": state.MaxTokens, "comments": state.Comments, "head": state.Head, "active": state.Active, "finished": state.Finished, "cancelled": state.Cancelled})
}
func github(w http.ResponseWriter, r *http.Request) {
	state.Lock()
	defer state.Unlock()
	path := r.URL.Path
	if strings.Contains(path, "/contents/") {
		json.NewEncoder(w).Encode(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(sampleSQL))})
		return
	}
	if strings.HasSuffix(path, "/files") {
		json.NewEncoder(w).Encode([]any{map[string]string{"filename": "queries/orders.sql", "status": "modified", "patch": "@@ -1,4 +1,4 @@\n SELECT id, status, created_at\n FROM orders\n-WHERE user_id = 1 AND status = 'paid'\n+WHERE user_id = 42 AND status = 'paid'\n ORDER BY created_at DESC LIMIT 20;"}})
		return
	}
	if strings.Contains(path, "/comments") {
		if r.Method == "GET" {
			if state.Comments == nil {
				state.Comments = []map[string]any{}
			}
			json.NewEncoder(w).Encode(state.Comments)
			return
		}
		var data map[string]any
		json.NewDecoder(r.Body).Decode(&data)
		if r.Method == "POST" {
			data["id"] = len(state.Comments) + 1
			state.Comments = append(state.Comments, data)
		} else if r.Method == "PATCH" {
			id, _ := strconv.Atoi(path[strings.LastIndex(path, "/")+1:])
			if id < 1 || id > len(state.Comments) {
				http.Error(w, "missing comment", 404)
				return
			}
			state.Comments[id-1]["body"] = data["body"]
			data["id"] = id
		}
		json.NewEncoder(w).Encode(data)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"number": 1, "head": map[string]any{"sha": state.Head, "repo": map[string]string{"full_name": "17hwliao/sql-sentinel"}}})
}
