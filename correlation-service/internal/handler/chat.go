package handler

import (
	"encoding/json"
	"net/http"

	"github.com/pulsetrace/correlation-service/internal/llm"
)

type ChatHandler struct {
	provider llm.Provider
}

func NewChatHandler(provider llm.Provider) *ChatHandler {
	return &ChatHandler{provider: provider}
}

func (h *ChatHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/chat", h.HandleChat)
}

type ChatRequest struct {
	Message string `json:"message"`
}

func (h *ChatHandler) HandleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// In a real app, we would load chat history and live telemetry context here.
	messages := []llm.Message{
		{Role: "user", Content: req.Message},
	}

	resp, err := h.provider.Chat(r.Context(), messages)
	if err != nil {
		// Fallback to hardcoded mock if Ollama isn't running locally to prevent total UI failure
		// (This ensures the demo still works even if the user hasn't started the heavy Ollama container)
		mockResp := llm.Response{
			Text: "I am unable to reach the local Ollama LLM endpoint. However, if this were working, I would analyze the Quickwit logs and Neo4j topology. Would you like me to rollback `cart-service` anyway?",
			Action: &llm.Action{
				Title:       "Execute Rollback (Fallback Mock)",
				Description: "Rollback cart-service to stable version",
				ActionLabel: "Confirm Rollback",
				Type:        "ROLLBACK",
				Target:      "cart-service",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResp)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

