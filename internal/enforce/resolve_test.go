package enforce

import (
	"net"
	"reflect"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeDNS responde a qualquer consulta A com um CNAME seguido de um IP,
// como um servidor recursivo de verdade faz.
func fakeDNS(t *testing.T) string {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var req dnsmessage.Message
			if req.Unpack(buf[:n]) != nil {
				continue
			}
			q := req.Questions[0]
			resp := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: req.ID, Response: true},
				Questions: req.Questions,
			}
			if q.Type == dnsmessage.TypeA {
				target := dnsmessage.MustNewName("youtube-ui.l.google.com.")
				resp.Answers = []dnsmessage.Resource{
					{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET},
						Body: &dnsmessage.CNAMEResource{CNAME: target}},
					{Header: dnsmessage.ResourceHeader{Name: target, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
						Body: &dnsmessage.AResource{A: [4]byte{142, 250, 200, 14}}},
				}
			}
			out, _ := resp.Pack()
			pc.WriteTo(out, addr)
		}
	}()
	return pc.LocalAddr().String()
}

func TestResolveIPsIgnoresHostsFile(t *testing.T) {
	old := DNSServers
	DNSServers = []string{fakeDNS(t)}
	defer func() { DNSServers = old }()

	got := ResolveIPs([]string{"www.youtube.com", "youtube.com"})
	if !reflect.DeepEqual(got, []string{"142.250.200.14"}) {
		t.Fatalf("got %v", got)
	}
}
