package enforce

import (
	"bytes"
	"log"
	"os"
	"slices"
	"time"

	"github.com/pedroivy/focolock/internal/sites"
	"github.com/pedroivy/focolock/internal/state"
)

const HostsPath = "/etc/hosts"

// IPRefresh: com que frequência re-resolvemos os IPs dos sites (CDNs mudam).
const IPRefresh = 10 * time.Minute

// AllowRefresh: as exceções ao bloqueio da internet são re-resolvidas mais
// vezes, porque um IP que mudou deixa o site permitido inacessível.
const AllowRefresh = 2 * time.Minute

// Enforcer mantém o sistema de acordo com a lista de domínios ativos.
// Apply é chamado a cada poucos segundos: se alguém editar o /etc/hosts ou
// limpar o firewall, o bloqueio volta no próximo ciclo.
type Enforcer struct {
	BlockIPs  bool   // também bloquear por IP no firewall (mais forte, pode afetar CDNs compartilhadas)
	HostsFile string // padrão: /etc/hosts

	lastDomains []string
	lastAllow   []string
	lastPF      string
	ips         []string
	ipsAt       time.Time
	allowIPs    []string
	allowAt     time.Time
}

// Apply bloqueia domains. Se domains contiver sites.Internet, allow são os
// sites que continuam acessíveis.
func (e *Enforcer) Apply(domains, allow []string) {
	changed := !slices.Equal(domains, e.lastDomains)
	blockAll := slices.Contains(domains, sites.Internet)
	wasBlockingAll := slices.Contains(e.lastDomains, sites.Internet)
	if !blockAll {
		allow = nil
	}
	allowChanged := !slices.Equal(allow, e.lastAllow)
	var hosts []string
	if len(domains) > 0 {
		hosts = append(sites.Expand(domains), sites.DoHHosts...)
	}

	if e.writeHosts(hosts) || changed {
		flushDNS()
	}

	var ips, doh []string
	if len(domains) > 0 {
		doh = sites.DoHIPs
		if e.BlockIPs {
			if changed || time.Since(e.ipsAt) > IPRefresh {
				e.ips, e.ipsAt = ResolveIPs(sites.Expand(domains)), time.Now()
			}
			ips = e.ips
		}
	}
	var allowIPs []string
	if len(allow) > 0 && e.BlockIPs {
		if allowChanged || time.Since(e.allowAt) > AllowRefresh {
			e.allowIPs, e.allowAt = ResolveAllowIPs(sites.Expand(allow)), time.Now()
		}
		allowIPs = e.allowIPs
	}
	rules := RenderPF(ips, doh, blockAll, allowIPs)
	if rules != e.lastPF || !pfIntact(len(domains) > 0) {
		if err := loadPF(rules, len(domains) > 0); err != nil {
			log.Printf("pf: %v", err)
		} else {
			e.lastPF = rules
			// Conexões já abertas (HTTP/2, websockets) sobreviveriam ao bloqueio.
			if blockAll && (!wasBlockingAll || allowChanged) {
				killAllStates()
			} else {
				killStates(ips)
			}
		}
	}
	if changed || allowChanged {
		log.Printf("sites bloqueados agora: %v (exceções: %v)", domains, allow)
	}
	e.lastDomains, e.lastAllow = domains, allow
}

// writeHosts reescreve o /etc/hosts só se o conteúdo divergir. Retorna true se escreveu.
func (e *Enforcer) writeHosts(hosts []string) bool {
	path := e.HostsFile
	if path == "" {
		path = HostsPath
	}
	cur, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("hosts: %v", err)
		return false
	}
	want := RenderHosts(cur, hosts)
	if bytes.Equal(cur, want) {
		return false
	}
	if err := state.WriteAtomic(path, want, 0o644); err != nil {
		log.Printf("hosts: %v", err)
		return false
	}
	return true
}
