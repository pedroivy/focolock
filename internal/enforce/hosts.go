// Package enforce aplica o bloqueio no sistema: /etc/hosts + firewall (pf).
package enforce

import (
	"bytes"
	"strings"
)

const (
	beginMarker = "# >>> focolock >>> (gerenciado automaticamente; editar não adianta)"
	endMarker   = "# <<< focolock <<<"
)

// RenderHosts devolve o conteúdo do hosts com a seção do focolock substituída.
// Com hosts vazio, a seção é removida. O resto do arquivo é preservado.
func RenderHosts(current []byte, hosts []string) []byte {
	var out bytes.Buffer
	inside := false
	for _, line := range strings.SplitAfter(string(current), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "# >>> focolock >>>"):
			inside = true
		case trimmed == endMarker:
			inside = false
		case !inside:
			out.WriteString(line)
		}
	}
	if len(hosts) == 0 {
		return out.Bytes()
	}
	if out.Len() > 0 && !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
		out.WriteByte('\n')
	}
	out.WriteString(beginMarker + "\n")
	for _, h := range hosts {
		out.WriteString("0.0.0.0 " + h + "\n")
		out.WriteString("::0 " + h + "\n")
	}
	out.WriteString(endMarker + "\n")
	return out.Bytes()
}
