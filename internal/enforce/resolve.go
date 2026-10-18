package enforce

import (
	"context"
	"net"
	"sort"
	"sync"
	"time"
)

// Resolver externo: consulta 1.1.1.1/8.8.8.8 direto, sem passar pelo /etc/hosts
// (que já aponta os sites bloqueados para 0.0.0.0).
var externalResolver = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		d := net.Dialer{Timeout: 3 * time.Second}
		for _, srv := range []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"} {
			if c, err := d.DialContext(ctx, "udp", srv); err == nil {
				return c, nil
			}
		}
		return d.DialContext(ctx, network, "1.1.1.1:53")
	},
}

// ResolveIPs resolve os hostnames em paralelo e devolve IPs únicos e ordenados.
// Falhas individuais são ignoradas (o /etc/hosts continua bloqueando o nome).
func ResolveIPs(hosts []string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var mu sync.Mutex
	set := map[string]bool{}
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for _, h := range hosts {
		wg.Add(1)
		go func(h string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			addrs, err := externalResolver.LookupIPAddr(ctx, h)
			if err != nil {
				return
			}
			mu.Lock()
			for _, a := range addrs {
				if a.IP.IsGlobalUnicast() {
					set[a.IP.String()] = true
				}
			}
			mu.Unlock()
		}(h)
	}
	wg.Wait()
	out := make([]string, 0, len(set))
	for ip := range set {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}
