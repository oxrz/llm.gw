package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"llama.gw/internal"
)

func main() {
	configPath := flag.String("config", os.Getenv("LLAMAD_CONFIG"), "path to models config")
	flag.Parse()
	internal.MustLoadConfig(*configPath)

	backendURL, _ := url.Parse(internal.BackendURL(""))
	revProxy := httputil.NewSingleHostReverseProxy(backendURL)

	http.HandleFunc("/v1/models", internal.CORS(func(w http.ResponseWriter, r *http.Request) {
		internal.WriteModelsList(w)
	}))

	http.HandleFunc("/", internal.CORS(func(w http.ResponseWriter, r *http.Request) {
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
					internal.EnsureModelReady(alias, internal.DefaultSwitchTimeout)
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
