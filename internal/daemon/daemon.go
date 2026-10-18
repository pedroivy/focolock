// Package daemon é o processo root que mantém os bloqueios valendo.
package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/pedroivy/focolock/internal/api"
	"github.com/pedroivy/focolock/internal/clock"
	"github.com/pedroivy/focolock/internal/enforce"
	"github.com/pedroivy/focolock/internal/guard"
	"github.com/pedroivy/focolock/internal/sites"
	"github.com/pedroivy/focolock/internal/state"
)

const (
	tickEvery  = 2 * time.Second
	saveEvery  = 30 * time.Second
	ntpEvery   = 5 * time.Minute
	guardEvery = 5 * time.Second
	// MaxLock evita um erro de digitação virar um bloqueio de anos.
	MaxLock = 90 * 24 * time.Hour
)

type Daemon struct {
	tickMu sync.Mutex // serializa tick(): o Enforcer não é concorrente
	mu     sync.Mutex
	st     *state.State
	clock  *clock.Clock
	store  state.Store
	enf    *enforce.Enforcer
	kick   chan struct{} // pede um tick imediato após uma mudança
}

func Run() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("o daemon precisa rodar como root")
	}
	st := guard.Store.Load()
	d := &Daemon{
		st:    st,
		clock: clock.New(st.ClockOffset, st.LastTrusted),
		store: guard.Store,
		enf:   &enforce.Enforcer{BlockIPs: true},
		kick:  make(chan struct{}, 1),
	}
	// Aplica imediatamente, antes de qualquer rede: no boot o bloqueio já vale.
	d.tick()
	go d.syncNTP()

	ln, err := listen()
	if err != nil {
		return err
	}
	go d.serve(ln)
	go d.serveWeb()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	tick := time.NewTicker(tickEvery)
	save := time.NewTicker(saveEvery)
	ntp := time.NewTicker(ntpEvery)
	grd := time.NewTicker(guardEvery)
	for {
		select {
		case <-tick.C:
			d.tick()
		case <-d.kick:
			d.tick()
		case <-save.C:
			d.save()
		case <-ntp.C:
			go d.syncNTP()
		case <-grd.C:
			if d.guarded() {
				guard.Ensure()
			}
		case s := <-sigs:
			// De propósito NÃO limpamos /etc/hosts nem o firewall: matar o
			// daemon não desbloqueia nada.
			log.Printf("recebido %v; salvando estado e saindo (bloqueios continuam aplicados)", s)
			d.save()
			ln.Close()
			return nil
		}
	}
}

func listen() (net.Listener, error) {
	_ = os.Remove(api.SocketPath)
	ln, err := net.Listen("unix", api.SocketPath)
	if err != nil {
		return nil, err
	}
	// Qualquer usuário pode criar bloqueios; ninguém pode removê-los.
	if err := os.Chmod(api.SocketPath, 0o666); err != nil {
		return nil, err
	}
	return ln, nil
}

func (d *Daemon) tick() {
	d.tickMu.Lock()
	defer d.tickMu.Unlock()
	if jump := d.clock.Tick(); jump != 0 {
		log.Printf("mudança de hora detectada (%v); compensando", jump)
		go d.syncNTP()
	}
	now := d.clock.Now()
	d.mu.Lock()
	pruned := d.st.Prune(now)
	active := d.st.ActiveSites(now)
	d.mu.Unlock()
	if pruned {
		d.save()
	}
	d.enf.Apply(active)
}

func (d *Daemon) syncNTP() {
	if err := d.clock.SyncNTP(); err != nil {
		log.Printf("ntp: %v (usando relógio local compensado)", err)
	}
}

func (d *Daemon) guarded() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st.Guarded(d.clock.Now())
}

func (d *Daemon) save() {
	d.mu.Lock()
	d.st.ClockOffset = d.clock.Offset()
	d.st.LastTrusted = d.clock.Now()
	data := *d.st
	d.mu.Unlock()
	if err := d.store.Save(&data); err != nil {
		log.Printf("salvar estado: %v", err)
	}
}

func (d *Daemon) serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			var req api.Request
			var resp *api.Response
			if err := json.NewDecoder(conn).Decode(&req); err != nil {
				resp = &api.Response{Error: "requisição inválida"}
			} else {
				resp = d.Handle(req)
			}
			_ = json.NewEncoder(conn).Encode(resp)
		}()
	}
}

// Handle processa uma requisição. Exportado para testes.
func (d *Daemon) Handle(req api.Request) *api.Response {
	now := d.clock.Now()
	msg, err := d.apply(req, now)
	if err != nil {
		return &api.Response{Error: err.Error(), Now: now}
	}
	if req.Cmd != api.CmdStatus {
		d.save()
		select {
		case d.kick <- struct{}{}:
		default:
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	snapshot := *d.st
	return &api.Response{
		OK:      true,
		Message: msg,
		Now:     now,
		Active:  d.st.ActiveSites(now),
		Guarded: d.st.Guarded(now),
		State:   &snapshot,
	}
}

func (d *Daemon) apply(req api.Request, now time.Time) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch req.Cmd {
	case api.CmdStatus:
		return "", nil

	case api.CmdLock:
		list, err := sites.NormalizeAll(req.Sites)
		if err != nil {
			return "", err
		}
		end, err := lockEnd(req, now)
		if err != nil {
			return "", err
		}
		l := state.Lock{ID: state.NewID(), Sites: list, Start: now, End: end}
		d.st.Locks = append(d.st.Locks, l)
		log.Printf("novo bloqueio %s até %s: %v", l.ID, end.Format(time.RFC3339), list)
		return fmt.Sprintf("Bloqueado até %s.", end.In(loc(req.TZ)).Format("02/01 15:04")), nil

	case api.CmdScheduleAdd:
		list, err := sites.NormalizeAll(req.Sites)
		if err != nil {
			return "", err
		}
		from, err := state.ParseHHMM(req.From)
		if err != nil {
			return "", err
		}
		to, err := state.ParseHHMM(req.To)
		if err != nil {
			return "", err
		}
		if len(req.Days) == 0 {
			return "", fmt.Errorf("informe os dias")
		}
		sc := state.Schedule{
			ID: state.NewID(), Name: req.Name, Sites: list, Days: req.Days,
			From: from, To: to, TZ: loc(req.TZ).String(), Created: now,
		}
		d.st.Schedules = append(d.st.Schedules, sc)
		log.Printf("novo agendamento %s %q", sc.ID, sc.Name)
		return fmt.Sprintf("Agendamento %s criado.", sc.ID), nil

	case api.CmdScheduleRemove:
		at, err := d.st.RequestRemoval(req.ID, now)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Remoção pedida. O agendamento continua valendo até %s (e até o fim da janela ativa, se houver).",
			at.In(loc(req.TZ)).Format("02/01 15:04")), nil
	}
	return "", fmt.Errorf("comando desconhecido %q", req.Cmd)
}

func loc(tz string) *time.Location {
	if l, err := time.LoadLocation(tz); err == nil && tz != "" {
		return l
	}
	return time.Local
}

func lockEnd(req api.Request, now time.Time) (time.Time, error) {
	var end time.Time
	switch {
	case req.For > 0 && req.Until != "":
		return end, fmt.Errorf("use --por OU --ate, não os dois")
	case req.For > 0:
		end = now.Add(req.For)
	case req.Until != "":
		m, err := state.ParseHHMM(req.Until)
		if err != nil {
			return end, err
		}
		t := now.In(loc(req.TZ))
		end = time.Date(t.Year(), t.Month(), t.Day(), m/60, m%60, 0, 0, t.Location())
		if !end.After(now) {
			end = end.AddDate(0, 0, 1)
		}
	default:
		return end, fmt.Errorf("informe --por (ex.: 2h) ou --ate (ex.: 18:00)")
	}
	if end.Sub(now) > MaxLock {
		return end, fmt.Errorf("bloqueio máximo é de %d dias", int(MaxLock.Hours()/24))
	}
	if end.Sub(now) < time.Minute {
		return end, fmt.Errorf("bloqueio mínimo é de 1 minuto")
	}
	return end, nil
}
