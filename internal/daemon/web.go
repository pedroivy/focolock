package daemon

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/pedroivy/focolock/internal/api"
)

// WebAddr é onde a janela do app é servida. Só escuta no próprio Mac.
const WebAddr = "127.0.0.1:47811"

//go:embed web/index.html
var indexHTML []byte

// serveWeb serve a interface gráfica. Proteções:
//   - Host precisa ser exatamente WebAddr (impede DNS rebinding);
//   - toda chamada à API exige um token embutido na página. Um site qualquer
//     aberto no navegador não consegue ler a página (mesma origem), então não
//     consegue criar bloqueios em seu nome.
func (d *Daemon) serveWeb() {
	tok := make([]byte, 16)
	_, _ = rand.Read(tok)
	token := hex.EncodeToString(tok)
	page := bytes.Replace(indexHTML, []byte("__FOCOLOCK_TOKEN__"), []byte(token), 1)
	if err := os.WriteFile(api.TokenPath, []byte(token), 0o644); err != nil {
		log.Printf("web: gravar token: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; frame-ancestors 'none'")
		_, _ = w.Write(page)
	})
	mux.HandleFunc("POST /api", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Focolock-Token") != token {
			http.Error(w, "token inválido; recarregue a página", http.StatusForbidden)
			return
		}
		var req api.Request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.Handle(req))
	})

	srv := &http.Server{
		Addr: WebAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host != WebAddr && r.Host != "localhost:47811" {
				http.Error(w, "host inválido", http.StatusForbidden)
				return
			}
			mux.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	for {
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("web: %v; tentando de novo em 5s", err)
		}
		time.Sleep(5 * time.Second)
	}
}
