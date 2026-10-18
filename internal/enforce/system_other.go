//go:build !darwin

package enforce

// Fora do macOS (desenvolvimento/testes) só o /etc/hosts é usado.
func flushDNS()                              {}
func pfIntact(expectRules bool) bool         { return true }
func loadPF(rules string, enable bool) error { return nil }
func killStates(ips []string)                {}
func killAllStates()                         {}
