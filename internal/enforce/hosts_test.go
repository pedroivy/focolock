package enforce

import (
	"strings"
	"testing"
)

const original = "127.0.0.1 localhost\n255.255.255.255 broadcasthost\n::1 localhost\n"

func TestRenderHostsRoundTrip(t *testing.T) {
	blocked := RenderHosts([]byte(original), []string{"x.com", "www.x.com"})
	s := string(blocked)
	if !strings.HasPrefix(s, original) || !strings.Contains(s, "0.0.0.0 x.com\n") || !strings.Contains(s, "::0 www.x.com\n") {
		t.Fatalf("hosts inesperado:\n%s", s)
	}
	// Idempotente.
	if again := RenderHosts(blocked, []string{"x.com", "www.x.com"}); string(again) != s {
		t.Fatalf("não idempotente:\n%s", again)
	}
	// Remover restaura o original.
	if cleared := RenderHosts(blocked, nil); string(cleared) != original {
		t.Fatalf("não restaurou:\n%q", cleared)
	}
}

func TestRenderHostsNoTrailingNewline(t *testing.T) {
	got := string(RenderHosts([]byte("127.0.0.1 localhost"), []string{"a.com"}))
	if !strings.HasPrefix(got, "127.0.0.1 localhost\n# >>> focolock") {
		t.Fatalf("got %q", got)
	}
}

func TestRenderPF(t *testing.T) {
	got := RenderPF([]string{"1.2.3.4"}, []string{"8.8.8.8"}, false, nil)
	for _, want := range []string{"<focolock_sites> const { 1.2.3.4 }", "to <focolock_doh> port 443"} {
		if !strings.Contains(got, want) {
			t.Errorf("faltou %q em:\n%s", want, got)
		}
	}
	if strings.Contains(RenderPF(nil, nil, false, nil), "block") {
		t.Error("sem sites não deveria haver regras")
	}
	all := RenderPF([]string{"1.2.3.4"}, []string{"8.8.8.8"}, true, nil)
	// O loopback tem de passar antes do bloqueio total, senão a app perde o serviço.
	if i, j := strings.Index(all, "pass out quick on lo0"), strings.Index(all, "block drop out quick all"); i < 0 || j < 0 || i > j {
		t.Errorf("regras de internet erradas:\n%s", all)
	}
}

func TestRenderPFInternetWithExceptions(t *testing.T) {
	got := RenderPF(nil, []string{"8.8.8.8"}, true, []string{"1.2.3.4", "2001:db8::1"})
	order := []string{
		"table <focolock_allow> const { 1.2.3.4, 2001:db8::1 }",
		"table <focolock_doh> const { 8.8.8.8 }",
		"pass out quick on lo0",
		"to <focolock_doh> port { 443, 853 }",
		"pass out quick proto { tcp, udp } from any to any port 53",
		"pass out quick from any to <focolock_allow>",
		"block drop out quick all",
	}
	last := -1
	for _, want := range order {
		i := strings.Index(got, want)
		if i < 0 || i < last {
			t.Fatalf("faltou ou fora de ordem %q em:\n%s", want, got)
		}
		last = i
	}
	// Sem exceções, nem o DNS passa.
	if strings.Contains(RenderPF(nil, nil, true, nil), "port 53") {
		t.Error("sem exceções o DNS não deveria passar")
	}
}
