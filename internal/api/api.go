// Package api é o protocolo entre a CLI e o daemon: JSON por um socket Unix.
//
// Repare que não existe comando para cancelar ou encurtar um bloqueio — só
// para criar. Por isso o socket pode ser aberto a qualquer usuário.
package api

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/pedroivy/focolock/internal/state"
)

const SocketPath = "/var/run/focolock.sock"

// TokenPath guarda o token da API web local. A app nativa lê daqui; sites
// abertos no navegador não conseguem ler ficheiros, então não conseguem
// criar bloqueios em seu nome.
const TokenPath = "/var/run/focolock.token"

const (
	CmdStatus         = "status"
	CmdLock           = "lock"
	CmdScheduleAdd    = "schedule_add"
	CmdScheduleRemove = "schedule_remove"
	CmdTimerAdd       = "timer_add"
	CmdTimerUse       = "timer_use"
	CmdTimerRemove    = "timer_remove"
)

type Request struct {
	Cmd   string   `json:"cmd"`
	Sites []string `json:"sites,omitempty"`

	// lock: duração OU horário de término "HH:MM" (próxima ocorrência, no fuso TZ).
	For   time.Duration `json:"for,omitempty"`
	Until string        `json:"until,omitempty"`

	// schedule_add
	Name string         `json:"name,omitempty"`
	Days []time.Weekday `json:"days,omitempty"`
	From string         `json:"from,omitempty"`
	To   string         `json:"to,omitempty"`
	TZ   string         `json:"tz,omitempty"`

	// schedule_remove, timer_use, timer_remove
	ID string `json:"id,omitempty"`

	// timer_add (usa também Name e Sites)
	Periods []Period `json:"periods,omitempty"`
	// timer_use: índice do período em Periods
	Period int `json:"period,omitempty"`
}

// Period é um período de um temporizador como chega da app/CLI.
type Period struct {
	From    string `json:"from"` // "HH:MM"
	To      string `json:"to"`   // "HH:MM"; "23:59" ou "24:00" para o fim do dia
	Minutes int    `json:"minutes"`
}

type Response struct {
	OK      bool         `json:"ok"`
	Error   string       `json:"error,omitempty"`
	Message string       `json:"message,omitempty"`
	Now     time.Time    `json:"now"`
	Active  []string     `json:"active,omitempty"`
	Guarded bool         `json:"guarded"`
	State   *state.State `json:"state,omitempty"`
}

// Call envia uma requisição ao daemon.
func Call(req Request, timeout time.Duration) (*Response, error) {
	conn, err := net.DialTimeout("unix", SocketPath, timeout)
	if err != nil {
		return nil, fmt.Errorf("não consegui falar com o daemon (%v). Ele está instalado? Rode: sudo focolock instalar", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return &resp, fmt.Errorf("%s", resp.Error)
	}
	return &resp, nil
}
