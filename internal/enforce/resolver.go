package enforce

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/pedroivy/focolock/internal/state"
)

// DefaultResolverDir: no macOS, um ficheiro /etc/resolver/<domínio> define o
// servidor DNS desse domínio e de todos os subdomínios.
const DefaultResolverDir = "/etc/resolver"

const resolverMarker = "# gerado pelo focolock"

// writeResolvers deixa em dir um ficheiro por domínio a apontar para o
// DNSProxy, e apaga os nossos que já não servem. Ficheiros que não fomos nós
// a criar nunca são tocados. Retorna true se mudou algo.
func writeResolvers(dir string, domains []string) bool {
	host, port, _ := strings.Cut(DNSProxyAddr, ":")
	want := []byte(resolverMarker + "\nnameserver " + host + "\nport " + port + "\n")
	keep := map[string]bool{}
	changed := false
	for _, d := range domains {
		keep[d] = true
		p := filepath.Join(dir, d)
		cur, err := os.ReadFile(p)
		if err == nil && (bytes.Equal(cur, want) || !bytes.HasPrefix(cur, []byte(resolverMarker))) {
			continue
		}
		if err := state.WriteAtomic(p, want, 0o644); err != nil {
			log.Printf("resolver: %v", err)
			continue
		}
		changed = true
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if keep[e.Name()] || e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if cur, err := os.ReadFile(p); err == nil && bytes.HasPrefix(cur, []byte(resolverMarker)) {
			_ = os.Remove(p)
			changed = true
		}
	}
	return changed
}
