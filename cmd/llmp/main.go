package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"

	"llama.gw/internal"
)

var httpClient = &http.Client{Timeout: 5 * time.Second}

func getCurrentModel() string {
	resp, err := httpClient.Get(fmt.Sprintf("http://127.0.0.1%s/status", internal.DaemonPort))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var s struct{ Model string `json:"model"` }
	json.NewDecoder(resp.Body).Decode(&s)
	return s.Model
}

func waitForModel(alias string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := httpClient.Get(fmt.Sprintf("http://127.0.0.1%s/status", internal.DaemonPort))
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		var s struct {
			Model     string `json:"model"`
			BackendOk bool   `json:"backend_ok"`
		}
		json.NewDecoder(resp.Body).Decode(&s)
		resp.Body.Close()
		if s.BackendOk && s.Model == alias {
			return true
		}
		time.Sleep(1 * time.Second)
	}
	return false
}

func switchIfNeeded(alias string) {
	current := getCurrentModel()
	if current == "" {
		return
	}
	if current == alias {
		return
	}
	log.Printf("🔄 %s → %s", current, alias)
	switchBody, _ := json.Marshal(map[string]string{"model": alias})
	resp, err := httpClient.Post(
		fmt.Sprintf("http://127.0.0.1%s/switch", internal.DaemonPort),
		"application/json",
		bytes.NewReader(switchBody),
	)
	if err != nil {
		log.Printf("❌ switch request failed: %v", err)
		return
	}
	defer resp.Body.Close()
	var sr struct {
		Status string `json:"status"`
	}
	json.NewDecoder(resp.Body).Decode(&sr)
	log.Printf("   status: %s", sr.Status)
	if !waitForModel(alias, 120*time.Second) {
		log.Printf("❌ timeout waiting for %s", alias)
	} else {
		log.Printf("✅ ready: %s", alias)
	}
}

func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next(w, r)
	}
}

func main() {
	// load models from .models.json
	configPath := flag.String("config", os.Getenv("LLAMAD_CONFIG"), "path to models config")
	flag.Parse()
	if *configPath == "" {
		*configPath = ".models.json"
	}

	if err := internal.LoadModels(*configPath); err != nil {
		log.Fatalf("failed to load models: %v", err)
	}

	backendURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1%s", internal.BackendPort))
	revProxy := httputil.NewSingleHostReverseProxy(backendURL)

	http.HandleFunc("/v1/models", corsMiddleware(func(w http.ResponseWriter, r *http.Request) {
		var models []map[string]string
		for name := range internal.ModelCommands {
			models = append(models, map[string]string{
				"id":       name,
				"object":   "model",
				"owned_by": "local",
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": models})
	}))

	http.HandleFunc("/", corsMiddleware(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if body == nil {
			body = []byte{}
		}
		var req map[string]interface{}
		if json.Unmarshal(body, &req) == nil {
			if model, ok := req["model"].(string); ok {
				alias := internal.ResolveModelName(model)
				if alias != "" {
					switchIfNeeded(alias)
				}
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		revProxy.ServeHTTP(w, r)
	}))

	log.Printf("🦙 llama.gw proxy %s → %s (async switch, CORS)", internal.ProxyPort, internal.BackendPort)
	log.Fatal(http.ListenAndServe(internal.ProxyPort, nil))
}
