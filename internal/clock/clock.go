// Package clock fornece um relógio "confiável": mudar a hora do Mac não
// encerra um bloqueio antes da hora.
//
// Como funciona:
//   - Um relógio monotônico que conta inclusive o tempo dormindo é comparado
//     com o relógio de parede a cada tique. Se a parede pular sem o monotônico
//     acompanhar, alguém mudou a hora: o pulo é compensado num offset.
//   - Quando há internet, o offset é recalibrado por NTP.
//   - Na inicialização, o tempo confiável nunca anda para trás em relação ao
//     último valor salvo.
package clock

import (
	"sync"
	"time"
)

// JumpTolerance: diferenças menores que isso entre parede e monotônico são ruído.
const JumpTolerance = 3 * time.Second

type Clock struct {
	mu       sync.Mutex
	offset   time.Duration // tempoConfiável = parede + offset
	lastWall time.Time
	lastMono time.Duration
	wall     func() time.Time
	mono     func() time.Duration
}

// New cria o relógio a partir do offset salvo e do último instante confiável visto.
func New(savedOffset time.Duration, lastTrusted time.Time) *Clock {
	return newWith(savedOffset, lastTrusted, wallNow, monotonic)
}

func newWith(savedOffset time.Duration, lastTrusted time.Time, wall func() time.Time, mono func() time.Duration) *Clock {
	c := &Clock{offset: savedOffset, wall: wall, mono: mono}
	c.lastWall, c.lastMono = wall(), mono()
	// Se a hora foi atrasada com o daemon desligado, não deixamos voltar.
	if now := c.lastWall.Add(c.offset); !lastTrusted.IsZero() && now.Before(lastTrusted) {
		c.offset = lastTrusted.Sub(c.lastWall)
	}
	return c
}

func wallNow() time.Time { return time.Now().Round(0) } // sem leitura monotônica do Go

// Tick detecta mudanças manuais de hora. Retorna o pulo compensado (0 se nenhum).
func (c *Clock) Tick() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	w, m := c.wall(), c.mono()
	jump := w.Sub(c.lastWall) - (m - c.lastMono)
	c.lastWall, c.lastMono = w, m
	if jump > JumpTolerance || jump < -JumpTolerance {
		c.offset -= jump
		return jump
	}
	return 0
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall().Add(c.offset)
}

func (c *Clock) Offset() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.offset
}

// SetTrusted recalibra com uma hora vinda de fonte externa (NTP).
func (c *Clock) SetTrusted(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset = t.Sub(c.wall())
}

var ntpServers = []string{"time.apple.com:123", "time.google.com:123", "pool.ntp.org:123"}

// SyncNTP tenta os servidores NTP em ordem; retorna erro se nenhum responder.
func (c *Clock) SyncNTP() error {
	var lastErr error
	for _, s := range ntpServers {
		t, err := QueryNTP(s, 3*time.Second)
		if err == nil {
			c.SetTrusted(t)
			return nil
		}
		lastErr = err
	}
	return lastErr
}
