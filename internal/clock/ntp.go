package clock

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

const ntpEpochOffset = 2208988800 // segundos entre 1900 e 1970

// QueryNTP faz uma consulta SNTP mínima (RFC 4330) e devolve a hora do servidor
// corrigida por metade do tempo de ida e volta.
func QueryNTP(addr string, timeout time.Duration) (time.Time, error) {
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return time.Time{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	req := make([]byte, 48)
	req[0] = 0x1B // LI=0, VN=3, Mode=3 (cliente)
	sent := time.Now()
	if _, err := conn.Write(req); err != nil {
		return time.Time{}, err
	}
	resp := make([]byte, 48)
	n, err := conn.Read(resp)
	if err != nil {
		return time.Time{}, err
	}
	rtt := time.Since(sent)
	if n < 48 || resp[0]&0x07 != 4 {
		return time.Time{}, fmt.Errorf("resposta NTP inválida de %s", addr)
	}
	if stratum := resp[1]; stratum == 0 || stratum > 15 {
		return time.Time{}, fmt.Errorf("NTP %s não sincronizado (stratum %d)", addr, stratum)
	}
	secs := binary.BigEndian.Uint32(resp[40:44])
	frac := binary.BigEndian.Uint32(resp[44:48])
	nanos := (int64(frac) * 1e9) >> 32
	t := time.Unix(int64(secs)-ntpEpochOffset, nanos)
	return t.Add(rtt / 2), nil
}
