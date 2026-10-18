package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pedroivy/focolock/internal/api"
	"github.com/pedroivy/focolock/internal/clock"
	"github.com/pedroivy/focolock/internal/enforce"
	"github.com/pedroivy/focolock/internal/state"
)

func newTestDaemon(t *testing.T) (*Daemon, string) {
	dir := t.TempDir()
	hosts := filepath.Join(dir, "hosts")
	if err := os.WriteFile(hosts, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Daemon{
		st:    &state.State{},
		clock: clock.New(0, time.Time{}),
		store: state.Store{Primary: filepath.Join(dir, "p.json"), Backup: filepath.Join(dir, "b.json")},
		enf:   &enforce.Enforcer{HostsFile: hosts},
	}, hosts
}

func TestLockBlocksAndPersists(t *testing.T) {
	d, hosts := newTestDaemon(t)
	resp := d.Handle(api.Request{Cmd: api.CmdLock, Sites: []string{"https://www.youtube.com/"}, For: time.Hour})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	if len(resp.Active) != 1 || resp.Active[0] != "youtube.com" || !resp.Guarded {
		t.Fatalf("resp = %+v", resp)
	}
	d.tick()
	data, _ := os.ReadFile(hosts)
	if !strings.Contains(string(data), "0.0.0.0 www.youtube.com") || !strings.Contains(string(data), "0.0.0.0 dns.google") {
		t.Fatalf("hosts:\n%s", data)
	}
	// Editar o hosts não adianta: o próximo tick reaplica.
	_ = os.WriteFile(hosts, []byte("127.0.0.1 localhost\n"), 0o644)
	d.tick()
	data, _ = os.ReadFile(hosts)
	if !strings.Contains(string(data), "youtube.com") {
		t.Fatal("bloqueio não foi reaplicado")
	}
	if got := d.store.Load(); len(got.Locks) != 1 {
		t.Fatal("estado não foi salvo")
	}
}

func TestLockValidation(t *testing.T) {
	d, _ := newTestDaemon(t)
	for _, req := range []api.Request{
		{Cmd: api.CmdLock, Sites: []string{"x.com"}},
		{Cmd: api.CmdLock, Sites: []string{"x.com"}, For: 365 * 24 * time.Hour},
		{Cmd: api.CmdLock, Sites: []string{"localhost"}, For: time.Hour},
		{Cmd: api.CmdLock, Sites: []string{"x.com"}, For: time.Hour, Until: "18:00"},
		{Cmd: "unlock"},
	} {
		if resp := d.Handle(req); resp.OK {
			t.Errorf("deveria falhar: %+v", req)
		}
	}
}

func TestLockUntilIsInFuture(t *testing.T) {
	d, _ := newTestDaemon(t)
	now := d.clock.Now()
	hhmm := now.Add(-time.Hour).In(time.UTC).Format("15:04") // já passou hoje -> amanhã
	resp := d.Handle(api.Request{Cmd: api.CmdLock, Sites: []string{"x.com"}, Until: hhmm, TZ: "UTC"})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	end := resp.State.Locks[0].End
	if !end.After(now) || end.Sub(now) > 24*time.Hour {
		t.Fatalf("end = %v, now = %v", end, now)
	}
}

func TestScheduleAddAndRemove(t *testing.T) {
	d, _ := newTestDaemon(t)
	resp := d.Handle(api.Request{Cmd: api.CmdScheduleAdd, Sites: []string{"x.com"}, Days: []time.Weekday{1}, From: "09:00", To: "18:00", TZ: "UTC"})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	id := resp.State.Schedules[0].ID
	resp = d.Handle(api.Request{Cmd: api.CmdScheduleRemove, ID: id})
	if !resp.OK || resp.State.Schedules[0].RemoveAfter == nil || !resp.Guarded {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestInternetLock(t *testing.T) {
	d, hosts := newTestDaemon(t)
	resp := d.Handle(api.Request{Cmd: api.CmdLock, Sites: []string{"internet", "x.com"}, For: time.Hour})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	if len(resp.Active) != 2 || resp.Active[0] != "*" {
		t.Fatalf("active = %v", resp.Active)
	}
	d.tick()
	data, _ := os.ReadFile(hosts)
	if strings.Contains(string(data), "0.0.0.0 *") || !strings.Contains(string(data), "0.0.0.0 x.com") {
		t.Fatalf("hosts:\n%s", data)
	}
}

func TestTimerAddUseRemove(t *testing.T) {
	d, hosts := newTestDaemon(t)
	now := d.clock.Now().In(time.UTC)
	m := now.Hour()*60 + now.Minute()
	// Um período que contém agora e outro (impossível de conter agora) para validar índices.
	from, to := state.FormatHHMM(m), "24:00"
	resp := d.Handle(api.Request{Cmd: api.CmdTimerAdd, Sites: []string{"x.com"}, TZ: "UTC",
		Periods: []api.Period{{From: from, To: to, Minutes: 1}}})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	if len(resp.Active) != 1 || resp.Active[0] != "x.com" || !resp.Guarded {
		t.Fatalf("resp = %+v", resp)
	}
	id := resp.State.Timers[0].ID
	d.tick()
	if data, _ := os.ReadFile(hosts); !strings.Contains(string(data), "0.0.0.0 x.com") {
		t.Fatalf("hosts:\n%s", data)
	}

	resp = d.Handle(api.Request{Cmd: api.CmdTimerUse, ID: id, Period: 0, TZ: "UTC"})
	if !resp.OK || len(resp.Active) != 0 {
		t.Fatalf("resp = %+v", resp)
	}
	d.tick()
	if data, _ := os.ReadFile(hosts); strings.Contains(string(data), "0.0.0.0 x.com") {
		t.Fatalf("deveria estar desbloqueado:\n%s", data)
	}
	if resp = d.Handle(api.Request{Cmd: api.CmdTimerUse, ID: id, Period: 0}); resp.OK {
		t.Fatal("não pode usar duas vezes")
	}
	if got := d.store.Load(); len(got.Timers) != 1 || len(got.Timers[0].Uses) != 1 {
		t.Fatal("pausa não foi salva")
	}

	resp = d.Handle(api.Request{Cmd: api.CmdTimerRemove, ID: id})
	if !resp.OK || resp.State.Timers[0].RemoveAfter == nil || !resp.Guarded {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestTimerValidation(t *testing.T) {
	d, _ := newTestDaemon(t)
	for _, req := range []api.Request{
		{Cmd: api.CmdTimerAdd, Sites: []string{"x.com"}},
		{Cmd: api.CmdTimerAdd, Sites: []string{"x.com"}, Periods: []api.Period{{From: "12:00", To: "06:00", Minutes: 10}}},
		{Cmd: api.CmdTimerAdd, Sites: []string{"x.com"}, Periods: []api.Period{{From: "06:00", To: "12:00"}}},
		{Cmd: api.CmdTimerAdd, Periods: []api.Period{{From: "06:00", To: "12:00", Minutes: 10}}},
		{Cmd: api.CmdTimerUse, ID: "nada"},
	} {
		if resp := d.Handle(req); resp.OK {
			t.Errorf("deveria falhar: %+v", req)
		}
	}
}
