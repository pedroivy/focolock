package enforce

import (
	"fmt"
	"strings"
)

// AnchorName fica sob "com.apple/*", que o /etc/pf.conf padrão do macOS já
// avalia, então não precisamos mexer no pf.conf do sistema.
const AnchorName = "com.apple/250.focolock"

// RenderPF gera as regras do anchor. blockedIPs são IPs dos sites bloqueados;
// dohIPs são bloqueados só na porta 443 (DNS-over-HTTPS / QUIC).
func RenderPF(blockedIPs, dohIPs []string) string {
	var b strings.Builder
	b.WriteString("# gerado pelo focolock\n")
	if len(blockedIPs) > 0 {
		fmt.Fprintf(&b, "table <focolock_sites> const { %s }\n", strings.Join(blockedIPs, ", "))
		b.WriteString("block drop out quick proto { tcp, udp } from any to <focolock_sites>\n")
	}
	if len(dohIPs) > 0 {
		fmt.Fprintf(&b, "table <focolock_doh> const { %s }\n", strings.Join(dohIPs, ", "))
		b.WriteString("block drop out quick proto { tcp, udp } from any to <focolock_doh> port 443\n")
		b.WriteString("block drop out quick proto { tcp, udp } from any to <focolock_doh> port 853\n")
	}
	return b.String()
}
