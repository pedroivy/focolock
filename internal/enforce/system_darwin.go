package enforce

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func flushDNS() {
	_, _ = run("/usr/bin/dscacheutil", "-flushcache")
	_, _ = run("/usr/bin/killall", "-HUP", "mDNSResponder")
}

// pfIntact diz se o anchor e o pf estão como deveriam (alguém pode ter rodado
// "pfctl -d" ou "pfctl -a ... -F all").
func pfIntact(expectRules bool) bool {
	out, err := run("/sbin/pfctl", "-a", AnchorName, "-s", "rules")
	if err != nil {
		return false
	}
	if !expectRules {
		return true
	}
	if !strings.Contains(out, "block") {
		return false
	}
	info, err := run("/sbin/pfctl", "-s", "info")
	return err == nil && strings.Contains(info, "Status: Enabled")
}

func loadPF(rules string, enable bool) error {
	cmd := exec.Command("/sbin/pfctl", "-a", AnchorName, "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pfctl -f: %v: %s", err, out)
	}
	if enable {
		// "pf already enabled" não é erro para nós.
		if out, err := run("/sbin/pfctl", "-e"); err != nil && !strings.Contains(out, "already enabled") {
			log.Printf("pfctl -e: %v", err)
		}
	}
	return nil
}

func killStates(ips []string) {
	for _, ip := range ips {
		any := "0.0.0.0/0"
		if strings.Contains(ip, ":") {
			any = "::/0"
		}
		_, _ = run("/sbin/pfctl", "-k", any, "-k", ip)
	}
}

// pfAddAllow acrescenta IPs à tabela de exceções sem recarregar as regras.
func pfAddAllow(ips []string) {
	args := append([]string{"-a", AnchorName, "-t", AllowTable, "-T", "add"}, ips...)
	if _, err := run("/sbin/pfctl", args...); err != nil {
		log.Printf("pf: %v", err)
	}
}

func killAllStates() {
	_, _ = run("/sbin/pfctl", "-F", "states")
}
