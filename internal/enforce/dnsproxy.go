package enforce

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// DNSProxyAddr é onde o serviço atende as consultas dos domínios permitidos.
// Durante um bloqueio de toda a internet, /etc/resolver/<domínio> manda o
// macOS perguntar aqui por esse domínio e todos os subdomínios. Assim
// qualquer subdomínio (ws.chatgpt.com, cdn.x.com…) é permitido no momento em
// que é resolvido, sem precisarmos de adivinhar a lista de nomes.
const DNSProxyAddr = "127.0.0.1:47853"

// DNSProxy encaminha as consultas dos domínios permitidos para um DNS real e
// avisa (Allow) os IPs da resposta antes de a devolver, para o firewall já os
// deixar passar quando o navegador ligar.
type DNSProxy struct {
	Upstream func() []string        // servidores "ip:53"
	Allow    func(ips []netip.Addr) // chamado antes de responder
	Timeout  time.Duration          // por servidor; padrão 3s
	query    func(server string, req []byte) ([]byte, error)

	mu      sync.Mutex
	domains []string
}

// SetDomains troca os domínios atendidos (e os seus subdomínios).
func (p *DNSProxy) SetDomains(domains []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.domains = append([]string(nil), domains...)
}

func (p *DNSProxy) matches(name string) bool {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, d := range p.domains {
		if name == d || strings.HasSuffix(name, "."+d) {
			return true
		}
	}
	return false
}

// Serve atende por UDP e TCP em addr até dar erro.
func (p *DNSProxy) Serve(addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer pc.Close()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	go p.serveTCP(ln)
	buf := make([]byte, 4096)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return err
		}
		req := append([]byte(nil), buf[:n]...)
		go func() {
			if resp := p.Handle(req); resp != nil {
				_, _ = pc.WriteTo(resp, from)
			}
		}()
	}
}

func (p *DNSProxy) serveTCP(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			var l uint16
			if binary.Read(c, binary.BigEndian, &l) != nil {
				return
			}
			req := make([]byte, l)
			if _, err := io.ReadFull(c, req); err != nil {
				return
			}
			resp := p.Handle(req)
			if resp == nil {
				return
			}
			_ = binary.Write(c, binary.BigEndian, uint16(len(resp)))
			_, _ = c.Write(resp)
		}()
	}
}

// Handle responde a uma consulta DNS em formato wire. Nomes fora dos
// domínios permitidos recebem NXDOMAIN sem sair do Mac.
func (p *DNSProxy) Handle(req []byte) []byte {
	var msg dnsmessage.Message
	if err := msg.Unpack(req); err != nil || len(msg.Questions) != 1 {
		return nil
	}
	if !p.matches(msg.Questions[0].Name.String()) {
		return reply(msg, dnsmessage.RCodeNameError)
	}
	query := p.query
	if query == nil {
		query = p.exchange
	}
	for _, srv := range p.Upstream() {
		resp, err := query(srv, req)
		if err != nil {
			continue
		}
		var ans dnsmessage.Message
		if ans.Unpack(resp) != nil || ans.ID != msg.ID {
			continue
		}
		var ips []netip.Addr
		for _, a := range ans.Answers {
			switch b := a.Body.(type) {
			case *dnsmessage.AResource:
				ips = append(ips, netip.AddrFrom4(b.A))
			case *dnsmessage.AAAAResource:
				ips = append(ips, netip.AddrFrom16(b.AAAA))
			}
		}
		if len(ips) > 0 && p.Allow != nil {
			p.Allow(ips)
		}
		return resp
	}
	return reply(msg, dnsmessage.RCodeServerFailure)
}

func (p *DNSProxy) exchange(server string, req []byte) ([]byte, error) {
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	conn, err := net.DialTimeout("udp", server, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func reply(msg dnsmessage.Message, code dnsmessage.RCode) []byte {
	msg.Header.Response = true
	msg.Header.RecursionAvailable = true
	msg.Header.RCode = code
	msg.Answers, msg.Authorities, msg.Additionals = nil, nil, nil
	out, err := msg.Pack()
	if err != nil {
		log.Printf("dns: %v", err)
		return nil
	}
	return out
}
