package enforce

import (
	"fmt"
	"strings"
)

// AnchorName fica sob "com.apple/*", que o /etc/pf.conf padrão do macOS já
// avalia, então não precisamos mexer no pf.conf do sistema.
const AnchorName = "com.apple/250.focolock"

// AllowTable guarda os IPs das exceções ao bloqueio da internet.
const AllowTable = "focolock_allow"

// RenderPF gera as regras do anchor. blockedIPs são IPs dos sites bloqueados;
// dohIPs são bloqueados só na porta 443 (DNS-over-HTTPS / QUIC). Com
// blockAll, toda a saída para a rede é bloqueada, exceto o próprio Mac
// (a app fala com o serviço por 127.0.0.1) e o DHCP (para a rede local
// continuar configurada e a internet voltar sozinha no fim). Com exceções
// (allow), só os allowIPs e o DNS comum, sem o qual não se chega lá, passam.
// A tabela de exceções não é const: o serviço acrescenta-lhe IPs à medida
// que o Mac resolve subdomínios das exceções (ver DNSProxy).
func RenderPF(blockedIPs, dohIPs []string, blockAll, allow bool, allowIPs []string) string {
	var b strings.Builder
	b.WriteString("# gerado pelo focolock\n")
	if blockAll {
		if allow {
			if len(allowIPs) > 0 {
				fmt.Fprintf(&b, "table <%s> persist { %s }\n", AllowTable, strings.Join(allowIPs, ", "))
			} else {
				fmt.Fprintf(&b, "table <%s> persist\n", AllowTable)
			}
			if len(dohIPs) > 0 {
				fmt.Fprintf(&b, "table <focolock_doh> const { %s }\n", strings.Join(dohIPs, ", "))
			}
		}
		b.WriteString("pass out quick on lo0 all\n")
		b.WriteString("pass out quick proto udp from any port 68 to any port 67\n")
		b.WriteString("pass out quick inet6 proto ipv6-icmp all\n")
		if allow {
			if len(dohIPs) > 0 {
				// O DoH contornaria o /etc/hosts; o DNS comum (53) tem de passar.
				b.WriteString("block drop out quick proto { tcp, udp } from any to <focolock_doh> port { 443, 853 }\n")
			}
			b.WriteString("pass out quick proto { tcp, udp } from any to any port 53\n")
			fmt.Fprintf(&b, "pass out quick from any to <%s>\n", AllowTable)
		}
		b.WriteString("block drop out quick all\n")
		return b.String()
	}
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
