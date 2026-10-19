package enforce

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func dnsQuery(t *testing.T, name string) []byte {
	t.Helper()
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: 42, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName(name), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
	}
	b, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeUpstream responde a qualquer consulta com 1.2.3.4.
func fakeUpstream(t *testing.T, calls *[]string) func(string, []byte) ([]byte, error) {
	return func(server string, req []byte) ([]byte, error) {
		*calls = append(*calls, server)
		var m dnsmessage.Message
		if err := m.Unpack(req); err != nil {
			t.Fatal(err)
		}
		m.Response = true
		m.Answers = []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{Name: m.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
			Body:   &dnsmessage.AResource{A: [4]byte{1, 2, 3, 4}},
		}}
		return m.Pack()
	}
}

func TestDNSProxyAllowsSubdomains(t *testing.T) {
	var calls []string
	var allowed []netip.Addr
	p := &DNSProxy{
		Upstream: func() []string { return []string{"9.9.9.9:53"} },
		Allow:    func(ips []netip.Addr) { allowed = append(allowed, ips...) },
	}
	p.query = fakeUpstream(t, &calls)
	p.SetDomains([]string{"chatgpt.com", "oaistatic.com"})

	for _, name := range []string{"chatgpt.com.", "ws.chatgpt.com.", "cdn.oaistatic.com."} {
		resp := p.Handle(dnsQuery(t, name))
		var m dnsmessage.Message
		if err := m.Unpack(resp); err != nil || m.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 1 {
			t.Fatalf("%s: %+v %v", name, m, err)
		}
	}
	if len(allowed) != 3 || allowed[0] != netip.MustParseAddr("1.2.3.4") {
		t.Fatalf("allowed = %v", allowed)
	}

	// Fora das exceções: NXDOMAIN e nada sai do Mac.
	calls = nil
	for _, name := range []string{"youtube.com.", "notchatgpt.com.", "chatgpt.com.evil.net."} {
		var m dnsmessage.Message
		if err := m.Unpack(p.Handle(dnsQuery(t, name))); err != nil || m.RCode != dnsmessage.RCodeNameError {
			t.Fatalf("%s: %+v %v", name, m, err)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("consultou o upstream para nomes bloqueados: %v", calls)
	}
}

func TestWriteResolvers(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "corp.local")
	if err := os.WriteFile(mine, []byte("nameserver 10.0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !writeResolvers(dir, []string{"chatgpt.com", "corp.local"}) {
		t.Fatal("deveria ter escrito")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "chatgpt.com"))
	if !strings.Contains(string(data), "nameserver 127.0.0.1\nport 47853") {
		t.Fatalf("resolver:\n%s", data)
	}
	if data, _ := os.ReadFile(mine); string(data) != "nameserver 10.0.0.1\n" {
		t.Fatal("ficheiro que não é nosso foi alterado")
	}
	if writeResolvers(dir, []string{"chatgpt.com", "corp.local"}) {
		t.Fatal("segunda escrita deveria ser no-op")
	}
	// Sem exceções, os nossos ficheiros saem e os dos outros ficam.
	if !writeResolvers(dir, nil) {
		t.Fatal("deveria ter apagado")
	}
	if _, err := os.Stat(filepath.Join(dir, "chatgpt.com")); !os.IsNotExist(err) {
		t.Fatal("resolver do focolock ficou para trás")
	}
	if _, err := os.Stat(mine); err != nil {
		t.Fatal("apagou um ficheiro que não é nosso")
	}
}

func TestEnforcerAllowsDynamicIPs(t *testing.T) {
	dir := t.TempDir()
	hosts := filepath.Join(dir, "hosts")
	_ = os.WriteFile(hosts, nil, 0o644)
	e := &Enforcer{HostsFile: hosts, ResolverDir: filepath.Join(dir, "resolver"), DNS: &DNSProxy{}}
	e.Apply([]string{"*"}, []string{"app.ivyflash.com", "chatgpt.com"})
	for _, d := range []string{"app.ivyflash.com", "ivyflash.com", "chatgpt.com", "oaistatic.com"} {
		if _, err := os.Stat(filepath.Join(dir, "resolver", d)); err != nil {
			t.Errorf("faltou o resolver de %s", d)
		}
		if !e.DNS.matches("x." + d) {
			t.Errorf("proxy não atende %s", d)
		}
	}
	e.AllowIPs([]netip.Addr{netip.MustParseAddr("1.2.3.4"), netip.MustParseAddr("127.0.0.1")})
	if got := e.allowSet([]string{"5.6.7.8"}, false); !slices.Equal(got, []string{"1.2.3.4", "5.6.7.8"}) {
		t.Fatalf("allowSet = %v", got)
	}
	// Fim do bloqueio: tudo limpo.
	e.Apply(nil, nil)
	if entries, _ := os.ReadDir(filepath.Join(dir, "resolver")); len(entries) != 0 {
		t.Fatalf("resolvers ficaram: %v", entries)
	}
	if e.DNS.matches("chatgpt.com") || len(e.allowSet(nil, false)) != 0 {
		t.Fatal("exceções ficaram ativas")
	}
}
