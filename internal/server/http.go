package server

import (
	"encoding/json"
	"net/http"

	"github.com/Adityarya11/StrataKV/internal/engine"
)

type Server struct {
	db *engine.DB
}

func NewServer(db *engine.DB) *Server {
	return &Server{db: db}
}

func (s *Server) Start(port string) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/put", s.handlePut)
	mux.HandleFunc("/get", s.handleGet)
	mux.HandleFunc("/delete", s.handleDelete)

	mux.HandleFunc("/compact", s.handleCompact)

	return http.ListenAndServe(port, mux)

}

type KVRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type KVResponse struct {
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
	Found bool   `json:"found,omitempty"`
	Error string `json:"error,omitempty"`
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req KVRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Key is required", http.StatusBadRequest)
		return
	}

	if req.Key == "" {
		http.Error(w, "key query parameter is required to request", http.StatusBadRequest)
		return
	}

	err := s.db.Put([]byte(req.Key), []byte(req.Value))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "success",
	})

}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "key query parameter is required to request", http.StatusBadRequest)
		return
	}

	val, found := s.db.Get([]byte(key))

	resp := KVResponse{
		Key:   key,
		Found: found,
	}

	if found {
		resp.Value = string(val)
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusNotFound)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)

}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "key query parameter is required to request", http.StatusBadRequest)
		return
	}

	err := s.db.Delete([]byte(key))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	err := s.db.Compact()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "compaction triggered successfully"})
}
