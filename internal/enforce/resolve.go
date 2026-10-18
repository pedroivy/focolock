package enforce

import (
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// DNSServers são consultados diretamente. Não dá para usar net.Resolver: até o
// resolvedor puro do Go lê o /etc/hosts antes, e lá os sites bloqueados já
// apontam para 0.0.0.0 — ficaríamos sem nenhum IP para o firewall.
var DNSServers = []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"}

// ResolveIPs resolve os hostnames em paralelo e devolve IPs únicos e ordenados.
// Falhas individuais são ignoradas (o /etc/hosts continua bloqueando o nome).
func ResolveIPs(hosts []string) []string {
	var mu sync.Mutex
	set := map[string]bool{}
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for _, h := range hosts {
		for _, t := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
			wg.Add(1)
			go func(h string, t dnsmessage.Type) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				ips, err := queryAny(h, t)
				if err != nil {
					return
				}
				mu.Lock()
				for _, ip := range ips {
					if ip.IsGlobalUnicast() {
						set[ip.String()] = true
					}
				}
				mu.Unlock()
			}(h, t)
		}
	}
	wg.Wait()
	out := make([]string, 0, len(set))
	for ip := range set {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

func queryAny(host string, t dnsmessage.Type) ([]netip.Addr, error) {
	var lastErr error
	for _, srv := range DNSServers {
		ips, err := query(srv, host, t)
		if err == nil {
			return ips, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// query faz uma consulta DNS por UDP e devolve os endereços da resposta
// (o servidor recursivo já segue os CNAMEs e inclui os A/AAAA finais).
func query(server, host string, t dnsmessage.Type) ([]netip.Addr, error) {
	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return nil, err
	}
	id := uint16(rand.Uint32())
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: id, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: name, Type: t, Class: dnsmessage.ClassINET}},
	}
	req, err := msg.Pack()
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("udp", server, 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	var resp dnsmessage.Message
	if err := resp.Unpack(buf[:n]); err != nil {
		return nil, err
	}
	if resp.ID != id {
		return nil, fmt.Errorf("resposta DNS com ID errado")
	}
	if resp.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("DNS %s: %v", host, resp.RCode)
	}
	var ips []netip.Addr
	for _, a := range resp.Answers {
		switch b := a.Body.(type) {
		case *dnsmessage.AResource:
			ips = append(ips, netip.AddrFrom4(b.A))
		case *dnsmessage.AAAAResource:
			ips = append(ips, netip.AddrFrom16(b.AAAA))
		}
	}
	return ips, nil
}
