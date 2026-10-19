package enforce

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pedroivy/focolock/internal/sites"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/publicsuffix"
)

// Descoberta automática: uma página permitida carrega scripts, estilos e
// imagens de outros domínios (CDNs). O serviço abre a página, vê de onde ela
// carrega coisas e permite esses domínios também, sem o utilizador ter de os
// adivinhar.

// DiscoverEvery: de quanto em quanto tempo se volta a ver cada exceção.
const DiscoverEvery = 6 * time.Hour

const (
	maxScripts   = 20
	maxBodyBytes = 8 << 20
)

// neverAuto: sites que nunca são abertos por descoberta automática, mesmo que
// uma página permitida os use (um vídeo embebido não pode abrir o YouTube).
// Só entram se o utilizador os puser explicitamente nas exceções.
var neverAuto = map[string]bool{
	"youtube.com": true, "youtu.be": true, "googlevideo.com": true, "ytimg.com": true,
	"facebook.com": true, "fbcdn.net": true, "instagram.com": true, "cdninstagram.com": true,
	"twitter.com": true, "x.com": true, "twimg.com": true, "t.co": true,
	"tiktok.com": true, "tiktokcdn.com": true, "reddit.com": true, "redditmedia.com": true,
	"redd.it": true, "netflix.com": true, "twitch.tv": true, "pinterest.com": true,
	"tumblr.com": true, "linkedin.com": true, "snapchat.com": true, "discord.com": true,
	"google.com": true, "bing.com": true, "amazon.com": true, "ebay.com": true,
	"whatsapp.com": true, "telegram.org": true, "vimeo.com": true, "dailymotion.com": true,
}

// cdnRoots: domínios de infraestrutura que um script pode referir e que não
// são "sites" por si (fontes, bibliotecas, ficheiros estáticos).
var cdnRoots = map[string]bool{
	"googleapis.com": true, "gstatic.com": true, "akamaized.net": true, "akamaihd.net": true,
	"fastly.net": true, "b-cdn.net": true, "jsdelivr.net": true, "unpkg.com": true,
	"azureedge.net": true, "cloudfront.net": true,
}

var cdnHosts = map[string]bool{
	"cdnjs.cloudflare.com": true, "challenges.cloudflare.com": true, "js.stripe.com": true,
}

var (
	tagRe     = regexp.MustCompile(`(?is)<(?:script|link|img|source|video|audio)\b[^>]*>`)
	tagHostRe = regexp.MustCompile(`(?i)(?:src|href|srcset)\s*=\s*["']?(?:https?:)?//([a-z0-9][a-z0-9.-]*\.[a-z]{2,63})`)
	scriptRe  = regexp.MustCompile(`(?is)<script\b[^>]*\bsrc\s*=\s*["']?([^"'\s>]+)`)
	jsHostRe  = regexp.MustCompile("(?i)[\"'`](?:https?:|wss?:)?//([a-z0-9][a-z0-9.-]*\\.[a-z]{2,63})")
)

func root(host string) string {
	if r, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return r
	}
	return host
}

// autoAllowed diz se um domínio encontrado pode ser aberto automaticamente.
func autoAllowed(host string, blocked []string) bool {
	if neverAuto[root(host)] {
		return false
	}
	for _, b := range blocked {
		if host == b || strings.HasSuffix(host, "."+b) {
			return false
		}
	}
	_, err := sites.Normalize(host)
	return err == nil
}

// HostsInHTML devolve os domínios de onde a página carrega scripts, estilos,
// imagens e media (links <a> para outros sites são ignorados).
func HostsInHTML(html string) []string {
	set := map[string]bool{}
	for _, tag := range tagRe.FindAllString(html, -1) {
		for _, m := range tagHostRe.FindAllStringSubmatch(tag, -1) {
			set[strings.ToLower(m[1])] = true
		}
	}
	return sortedSet(set)
}

// HostsInJS devolve os domínios citados num script que são do próprio site
// (mesmo domínio principal) ou de CDNs: um script cita muitos links que não
// são dependências, e esses não podem virar exceções.
func HostsInJS(js, site string) []string {
	siteRoot := root(site)
	set := map[string]bool{}
	for _, m := range jsHostRe.FindAllStringSubmatch(js, -1) {
		h := strings.ToLower(m[1])
		r := root(h)
		if r == siteRoot || cdnRoots[r] || cdnHosts[h] || privateSuffixHost(h) {
			set[h] = true
		}
	}
	return sortedSet(set)
}

// privateSuffixHost: nomes em sufixos privados como xyz.cloudfront.net ou
// abc.b-cdn.net são de um único cliente do CDN.
func privateSuffixHost(h string) bool {
	suffix, icann := publicsuffix.PublicSuffix(h)
	return !icann && strings.Contains(suffix, ".")
}

func scriptURLs(html string, base *url.URL) []*url.URL {
	var out []*url.URL
	for _, m := range scriptRe.FindAllStringSubmatch(html, -1) {
		if u, err := base.Parse(m[1]); err == nil && (u.Scheme == "https" || u.Scheme == "http") {
			out = append(out, u)
		}
	}
	return out
}

// Discover abre a página de site e devolve os domínios extra de que ela
// precisa. fetch tem de conseguir chegar ao site e a cada domínio aceite
// (o serviço abre-os no firewall antes de ligar; ver discoverFetch).
func Discover(site string, blocked []string, fetch func(u *url.URL) (string, error)) ([]string, error) {
	page, _ := url.Parse("https://" + site + "/")
	html, err := fetch(page)
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	accept := func(hosts []string) {
		for _, h := range hosts {
			if h != site && autoAllowed(h, blocked) {
				found[h] = true
			}
		}
	}
	accept(HostsInHTML(html))
	scripts := scriptURLs(html, page)
	if len(scripts) > maxScripts {
		scripts = scripts[:maxScripts]
	}
	for _, s := range scripts {
		h := s.Hostname()
		if h != site && !found[h] && root(h) != root(site) {
			continue // só seguimos scripts de sítios já aceites
		}
		if js, err := fetch(s); err == nil {
			accept(HostsInJS(js, site))
		}
	}
	return sortedSet(found), nil
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// discoverFetch descarrega um URL durante um bloqueio da internet: resolve o
// nome pelo DNS real, abre os IPs no firewall (o domínio já foi aceite) e só
// depois liga.
func (e *Enforcer) discoverFetch(u *url.URL) (string, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips := lookupAll(host)
			if len(ips) == 0 {
				return nil, fmt.Errorf("não resolveu %s", host)
			}
			e.AllowIPs(ips)
			var lastErr error
			for _, ip := range ips {
				c, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return c, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
		TLSHandshakeTimeout: 10 * time.Second,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		Timeout:   30 * time.Second,
		// Só redirecionamentos dentro do mesmo site: um redirecionamento não
		// pode abrir outro domínio no firewall.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || root(req.URL.Hostname()) != root(via[0].URL.Hostname()) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	return string(body), err
}

func lookupAll(host string) []netip.Addr {
	var out []netip.Addr
	for _, t := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		for _, srv := range UpstreamDNS() {
			if ips, err := query(srv, host, t); err == nil {
				out = append(out, ips...)
				break
			}
		}
	}
	return out
}
