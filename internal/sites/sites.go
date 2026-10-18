// Package sites normaliza domínios e gera as variantes que são bloqueadas.
package sites

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var hostRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// Nunca bloqueamos estes (quebrariam o sistema ou o próprio bloqueador).
var forbidden = map[string]bool{
	"localhost":    true,
	"apple.com":    true,
	"icloud.com":   true,
	"pool.ntp.org": true,
}

// Internet é o "site" especial que bloqueia toda a ligação à internet.
const Internet = "*"

// Normalize aceita "https://www.YouTube.com/watch?v=1" e devolve "youtube.com".
// "internet" ou "*" viram Internet.
func Normalize(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", fmt.Errorf("site vazio")
	}
	if s == Internet || s == "internet" || s == "toda a internet" {
		return Internet, nil
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("site inválido %q: %w", raw, err)
	}
	h := strings.TrimSuffix(u.Hostname(), ".")
	h = strings.TrimPrefix(h, "*.")
	h = strings.TrimPrefix(h, "www.")
	if !hostRe.MatchString(h) {
		return "", fmt.Errorf("site inválido %q", raw)
	}
	if forbidden[h] {
		return "", fmt.Errorf("o site %q não pode ser bloqueado", h)
	}
	return h, nil
}

// NormalizeAll normaliza, remove duplicados e ordena.
func NormalizeAll(raw []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, r := range raw {
		h, err := Normalize(r)
		if err != nil {
			return nil, err
		}
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("informe pelo menos um site")
	}
	sort.Strings(out)
	return out, nil
}

// NormalizeAllow normaliza as exceções ao bloqueio de toda a internet. Só faz
// sentido se sites contiver Internet; a lista pode ser vazia.
func NormalizeAllow(sites, raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	internet := false
	for _, s := range sites {
		internet = internet || s == Internet
	}
	if !internet {
		return nil, fmt.Errorf("exceções só existem no bloqueio de toda a internet")
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range raw {
		h, err := Normalize(r)
		if err != nil && !strings.Contains(err.Error(), "não pode ser bloqueado") {
			return nil, err
		}
		if h == Internet {
			return nil, fmt.Errorf("a exceção não pode ser toda a internet")
		}
		if h == "" || forbidden[h] {
			continue // nunca são bloqueados, não precisam de exceção
		}
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out, nil
}

// O /etc/hosts não aceita curingas, então cobrimos os subdomínios mais comuns.
var commonSubdomains = []string{"", "www.", "m.", "mobile.", "web.", "app.", "api."}

// Expand devolve todos os hostnames a serem bloqueados para os domínios dados.
func Expand(domains []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range domains {
		if d == Internet {
			continue
		}
		for _, p := range commonSubdomains {
			h := p + d
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Servidores de DNS-over-HTTPS. Navegadores com "DNS seguro" ignoram o
// /etc/hosts, então bloqueamos esses endpoints durante um bloqueio para forçar
// o navegador a voltar ao DNS do sistema.
var DoHHosts = []string{
	"dns.google", "dns.google.com", "8888.google",
	"cloudflare-dns.com", "mozilla.cloudflare-dns.com", "chrome.cloudflare-dns.com",
	"one.one.one.one", "1dot1dot1dot1.cloudflare-dns.com",
	"dns.quad9.net", "dns9.quad9.net", "dns10.quad9.net", "dns11.quad9.net",
	"doh.opendns.com", "dns.opendns.com", "doh.familyshield.opendns.com",
	"dns.nextdns.io", "doh.cleanbrowsing.org", "dns.adguard.com", "dns.adguard-dns.com",
	"doh.dns.sb", "dns.mullvad.net", "doh.mullvad.net",
	// iCloud Private Relay contorna o DNS do sistema no Safari.
	"mask.icloud.com", "mask-h2.icloud.com", "mask-api.icloud.com",
}

// DoHIPs são bloqueados apenas na porta 443 (DNS comum na 53 continua funcionando).
var DoHIPs = []string{
	"8.8.8.8", "8.8.4.4", "1.1.1.1", "1.0.0.1", "9.9.9.9", "149.112.112.112",
	"208.67.222.222", "208.67.220.220", "94.140.14.14", "94.140.15.15",
	"45.90.28.0", "45.90.30.0", "185.228.168.168", "185.222.222.222",
	"2001:4860:4860::8888", "2001:4860:4860::8844",
	"2606:4700:4700::1111", "2606:4700:4700::1001",
	"2620:fe::fe", "2620:fe::9",
}
