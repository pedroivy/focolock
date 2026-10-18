package daemon

import (
	"log"
	"os"
	"time"

	"github.com/pedroivy/focolock/internal/api"
	"github.com/pedroivy/focolock/internal/clock"
	"github.com/pedroivy/focolock/internal/guard"
	"github.com/pedroivy/focolock/internal/state"
)

// RunWatchdog vigia o daemon. Guarda em memória a última cópia do estado: se o
// daemon for derrubado e os arquivos de estado apagados, ele os reescreve
// antes de religar o daemon.
func RunWatchdog() error {
	var cached *state.State
	for {
		resp, err := api.Call(api.Request{Cmd: api.CmdStatus}, 2*time.Second)
		switch {
		case err == nil:
			cached = resp.State
			if resp.Guarded {
				guard.Ensure()
			}
		default:
			st := guard.Store.Load()
			if cached != nil {
				st = state.Merge(st, cached)
			}
			if st.Guarded(clock.New(st.ClockOffset, st.LastTrusted).Now()) {
				log.Printf("watchdog: daemon fora do ar com bloqueio ativo (%v); restaurando", err)
				if _, statErr := os.Stat(guard.Store.Primary); statErr != nil {
					_ = guard.Store.Save(st)
				}
				guard.Ensure()
			}
		}
		time.Sleep(5 * time.Second)
	}
}
