package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"llama.gw/internal"
)

var httpClient = &http.Client{Timeout: 5 * time.Second}

type Daemon struct {
	mu           sync.Mutex
	currentAlias string
	startTime    time.Time
	switching    bool
	queue        []string
}

func (d *Daemon) isBackendAlive() bool {
	resp, err := httpClient.Get(internal.HealthURL)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func (d *Daemon) isBackendReady() bool {
	resp, err := httpClient.Post(
		fmt.Sprintf("http://127.0.0.1%s/v1/chat/completions", internal.BackendPort),
		"application/json",
		strings.NewReader(`{"messages":[{"role":"user","content":"x"}],"max_tokens":1}`),
	)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// killBackend kills only the llama-server on the daemon's configured backend port.
func (d *Daemon) killBackend() {
	port := strings.TrimPrefix(internal.BackendPort, ":")
	pattern := fmt.Sprintf("llama-server.*--port %s", port)
	exec.Command("pkill", "-f", pattern).Run()
	for i := 0; i < 60; i++ {
		if !d.isBackendAlive() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	log.Printf("backend alive after SIGTERM, sending SIGKILL")
	exec.Command("pkill", "-9", "-f", pattern).Run()
	for i := 0; i < 10; i++ {
		if !d.isBackendAlive() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	log.Printf("WARNING: backend still alive after SIGKILL")
}

func (d *Daemon) startModelInternal(alias string) error {
	cmdStr := internal.ModelCommands[alias]
	if cmdStr == "" {
		return fmt.Errorf("unknown model: %s", alias)
	}

	if d.currentAlias != "" {
		d.killBackend()
	}

	port := strings.TrimPrefix(internal.BackendPort, ":")
	cmd := exec.Command("bash", "-c",
		fmt.Sprintf("%s --port %s >> ~/llama-server.log 2>&1", cmdStr, port))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s: %w", alias, err)
	}

	for i := 0; i < 120; i++ {
		time.Sleep(1 * time.Second)
		if d.isBackendReady() {
			log.Printf("✅ %s ready (%ds)", alias, i+1)
			return nil
		}
		if i%10 == 9 {
			log.Printf("   waiting for %s ... (%ds)", alias, i+1)
		}
	}
	return fmt.Errorf("timeout waiting for %s", alias)
}

func (d *Daemon) dequeueNext() {
	if len(d.queue) == 0 {
		return
	}
	next := d.queue[0]
	d.queue = d.queue[1:]
	d.switching = true
	go d.runSwitch(next)
}

func (d *Daemon) runSwitch(alias string) {
	log.Printf("🔄 switching to %s ...", alias)
	err := d.startModelInternal(alias)

	d.mu.Lock()
	if err == nil {
		d.currentAlias = alias
	} else {
		log.Printf("❌ switch to %s failed: %v", alias, err)
	}
	d.switching = false
	d.dequeueNext()
	d.mu.Unlock()
}

func (d *Daemon) runHeartbeat() {
	ticker := time.NewTicker(internal.HealthInterval)
	defer ticker.Stop()
	for range ticker.C {
		d.mu.Lock()
		if d.switching || d.currentAlias == "" {
			d.mu.Unlock()
			continue
		}
		alias := d.currentAlias
		d.mu.Unlock()

		if !d.isBackendAlive() {
			log.Printf("💔 heartbeat: restarting %s", alias)
			d.mu.Lock()
			if !d.switching {
				d.switching = true
				d.mu.Unlock()
				go d.runSwitch(alias)
			} else {
				d.mu.Unlock()
			}
		}
	}
}

func (d *Daemon) handleStatus(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	alias := d.currentAlias
	switching := d.switching
	uptime := time.Since(d.startTime).Round(time.Second).String()
	d.mu.Unlock()

	alive := d.isBackendAlive()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"model":      alias,
		"backend_ok": alive && !switching,
		"switching":  switching,
		"queued":     len(d.queue),
		"uptime":     uptime,
	})
}

func (d *Daemon) handleSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, 400)
		return
	}

	alias := internal.ResolveModelName(req.Model)
	if alias == "" {
		http.Error(w, fmt.Sprintf(`{"error":"unknown model: %s"}`, req.Model), 400)
		return
	}

	d.mu.Lock()

	if !d.switching && d.currentAlias == alias && d.isBackendAlive() {
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "already", "model": alias})
		return
	}

	if d.switching && d.currentAlias == alias {
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "accepted", "model": alias})
		return
	}
	for _, q := range d.queue {
		if q == alias {
			d.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":   "queued",
				"model":    alias,
				"position": len(d.queue),
			})
			return
		}
	}

	if d.switching {
		d.queue = append(d.queue, alias)
		pos := len(d.queue)
		d.mu.Unlock()
		log.Printf("📥 queued %s (position %d)", alias, pos)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "queued",
			"model":    alias,
			"position": pos,
		})
		return
	}

	d.switching = true
	d.mu.Unlock()

	go d.runSwitch(alias)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted", "model": alias})
}

func main() {
	configPath := flag.String("config", os.Getenv("LLAMAD_CONFIG"), "path to models config")
	flag.Parse()
	if err := internal.LoadModels(*configPath); err != nil {
		log.Fatalf("failed to load models: %v", err)
	}
	log.Printf("📋 loaded %d model(s), default: %s", len(internal.ModelCommands), internal.DefaultModel)

	d := &Daemon{startTime: time.Now()}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Printf("shutting down, killing backend...")
		d.killBackend()
		os.Exit(0)
	}()

	d.mu.Lock()
	d.switching = true
	d.mu.Unlock()
	log.Printf("🔄 loading default model %s ...", internal.DefaultModel)
	if err := d.startModelInternal(internal.DefaultModel); err != nil {
		log.Fatalf("failed to load default model: %v", err)
	}
	d.mu.Lock()
	d.currentAlias = internal.DefaultModel
	d.switching = false
	d.mu.Unlock()

	go d.runHeartbeat()

	mux := http.NewServeMux()
	mux.HandleFunc("/status", d.handleStatus)
	mux.HandleFunc("/switch", d.handleSwitch)

	log.Printf("🦙 llama.gw daemon on %s, backend %s", internal.DaemonPort, internal.BackendPort)
	log.Fatal(http.ListenAndServe(internal.DaemonPort, mux))
}
