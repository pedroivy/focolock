package clock

import (
	"testing"
	"time"
)

type fake struct {
	wall time.Time
	mono time.Duration
}

func (f *fake) w() time.Time            { return f.wall }
func (f *fake) m() time.Duration        { return f.mono }
func (f *fake) advance(d time.Duration) { f.wall = f.wall.Add(d); f.mono += d }

func TestManualClockChangeIsCompensated(t *testing.T) {
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	f := &fake{wall: base}
	c := newWith(0, time.Time{}, f.w, f.m)

	f.advance(10 * time.Second)
	c.Tick()
	// Usuário adianta a hora em 5 horas para "terminar" o bloqueio.
	f.wall = f.wall.Add(5 * time.Hour)
	if jump := c.Tick(); jump != 5*time.Hour {
		t.Fatalf("jump = %v", jump)
	}
	if got, want := c.Now(), base.Add(10*time.Second); !got.Equal(want) {
		t.Fatalf("Now = %v, want %v", got, want)
	}
	// Atrasar também é compensado.
	f.wall = f.wall.Add(-8 * time.Hour)
	c.Tick()
	if got, want := c.Now(), base.Add(10*time.Second); !got.Equal(want) {
		t.Fatalf("Now = %v, want %v", got, want)
	}
}

func TestSleepIsNotAJump(t *testing.T) {
	f := &fake{wall: time.Date(2026, 1, 1, 22, 0, 0, 0, time.UTC)}
	c := newWith(0, time.Time{}, f.w, f.m)
	f.advance(9 * time.Hour) // dormiu a noite toda; o monotônico contínuo também andou
	if jump := c.Tick(); jump != 0 {
		t.Fatalf("sleep detectado como pulo: %v", jump)
	}
}

func TestNeverGoesBackAcrossRestart(t *testing.T) {
	last := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	f := &fake{wall: last.Add(-3 * time.Hour)} // hora atrasada com o daemon desligado
	c := newWith(0, last, f.w, f.m)
	if c.Now().Before(last) {
		t.Fatalf("tempo confiável voltou: %v < %v", c.Now(), last)
	}
}
