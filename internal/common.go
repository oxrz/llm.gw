package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// HTTPClient is shared between llmd and llmp.
var HTTPClient = &http.Client{Timeout: 5 * time.Second}

// DefaultSwitchTimeout is the default time to wait for a model switch.
const DefaultSwitchTimeout = 120 * time.Second

// ── URL helpers ──────────────────────────────────────────────────────

// DaemonURL builds an absolute URL for the llmd daemon endpoint.
func DaemonURL(path string) string {
	return fmt.Sprintf("http://127.0.0.1%s%s", DaemonPort, path)
}

// BackendURL builds an absolute URL for the llama-server backend.
func BackendURL(path string) string {
	return fmt.Sprintf("http://127.0.0.1%s%s", BackendPort, path)
}

// ── Config loading ───────────────────────────────────────────────────

// MustLoadConfig loads models from the given config path.
// If configPath is empty, uses default lookup (binary-adjacent, then CWD).
// Fatals on error and logs the result.
func MustLoadConfig(configPath string) {
	if err := LoadModels(configPath); err != nil {
		log.Fatalf("failed to load models: %v", err)
	}
	log.Printf("📋 loaded %d model(s), default: %s", len(ModelCommands), DefaultModel)
}

// ── Daemon communication ─────────────────────────────────────────────

// GetCurrentModel returns the model currently loaded by the daemon.
func GetCurrentModel() string {
	resp, err := HTTPClient.Get(DaemonURL("/status"))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var s struct{ Model string `json:"model"` }
	json.NewDecoder(resp.Body).Decode(&s)
	return s.Model
}

// WaitForModel polls the daemon until the given model is ready or timeout.
func WaitForModel(alias string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := HTTPClient.Get(DaemonURL("/status"))
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

// SwitchToModel sends a switch request to the daemon.
func SwitchToModel(alias string) error {
	body, _ := json.Marshal(map[string]string{"model": alias})
	resp, err := HTTPClient.Post(DaemonURL("/switch"), "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("switch request failed: %w", err)
	}
	defer resp.Body.Close()
	var sr struct {
		Status string `json:"status"`
	}
	json.NewDecoder(resp.Body).Decode(&sr)
	log.Printf("   status: %s", sr.Status)
	return nil
}

// EnsureModelReady switches to the model (if needed) and waits for it to be ready.
func EnsureModelReady(alias string, timeout time.Duration) bool {
	current := GetCurrentModel()
	if current == "" {
		return false
	}
	if current == alias {
		return true
	}
	log.Printf("🔄 %s → %s", current, alias)
	if err := SwitchToModel(alias); err != nil {
		log.Printf("❌ %v", err)
		return false
	}
	if !WaitForModel(alias, timeout) {
		log.Printf("❌ timeout waiting for %s", alias)
		return false
	}
	log.Printf("✅ ready: %s", alias)
	return true
}

// ── HTTP handlers ────────────────────────────────────────────────────

// BackendModelMeta holds metadata from the running llama-server.
type BackendModelMeta map[string]interface{}

// FetchBackendModelMeta fetches /v1/models from the backend and returns
// the metadata for the currently loaded model, or nil on error.
func FetchBackendModelMeta() BackendModelMeta {
	resp, err := HTTPClient.Get(BackendURL("/v1/models"))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var data struct {
		Data []struct {
			Meta BackendModelMeta `json:"meta"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&data) != nil || len(data.Data) == 0 {
		return nil
	}
	meta := data.Data[0].Meta
	if meta != nil {
		// Fix: override n_ctx_train to match n_ctx (avoid context length mismatch).
		if nctx, ok := meta["n_ctx"]; ok {
			meta["n_ctx_train"] = nctx
		}
	}
	return meta
}

// parseCtxSize extracts the context size from a model command string
// by looking for the -c/--ctx-size flag. Returns 0 if not found.
func parseCtxSize(cmd string) int {
	re := regexp.MustCompile(`-c\s+(\d+)\s+-`)
	m := re.FindStringSubmatch(cmd)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// WriteModelsList writes an OpenAI-compatible /v1/models response.
// If backendMeta is non-nil, it is attached to the currently loaded model.
// For other models, context size is parsed from the model command.
func WriteModelsList(w http.ResponseWriter, backendMeta BackendModelMeta) {
	current := GetCurrentModel()
	var models []map[string]interface{}
	for name := range ModelCommands {
		m := map[string]interface{}{
			"id":       name,
			"object":   "model",
			"owned_by": "local",
		}
		if backendMeta != nil && name == current {
			m["meta"] = backendMeta
		} else if nctx := parseCtxSize(ModelCommands[name]); nctx > 0 {
			m["meta"] = BackendModelMeta{
				"n_ctx":       nctx,
				"n_ctx_train": nctx,
			}
		}
		models = append(models, m)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": models})
}

// CORS wraps a handler with CORS headers.
func CORS(next http.HandlerFunc) http.HandlerFunc {
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
