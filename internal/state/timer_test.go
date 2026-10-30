package state

import (
	"github.com/pedroivy/focolock/internal/sites"
	"reflect"
	"slices"
	"testing"
	"time"
)

// Manhã 06:00–12:00 com 10 min e noite 18:00–23:59 com 10 min.
func twoPeriods() Timer {
	return Timer{
		ID: "t", Sites: []string{"x.com"}, TZ: "America/Sao_Paulo",
		Periods: []TimerPeriod{{From: 6 * 60, To: 12 * 60, Minutes: 10}, {From: 18 * 60, To: 23*60 + 59, Minutes: 10}},
	}
}

func TestTimerBlocksByDefault(t *testing.T) {
	st := &State{Timers: []Timer{twoPeriods()}}
	for _, now := range []time.Time{at(5, 3, 0), at(5, 9, 0), at(5, 20, 0)} {
		if got := st.ActiveSites(now); !reflect.DeepEqual(got, []string{"x.com"}) {
			t.Fatalf("%v: active = %v", now, got)
		}
	}
	if !st.Guarded(at(5, 9, 0)) {
		t.Fatal("temporizador deveria proteger contra desinstalação")
	}
}

func TestTimerUseOncePerPeriodPerDay(t *testing.T) {
	st := &State{Timers: []Timer{twoPeriods()}}
	tm := func() Timer { return st.Timers[0] }

	if s, _ := tm().PeriodStatus(0, at(5, 9, 0)); s != PeriodAvailable {
		t.Fatalf("status = %s", s)
	}
	if s, _ := tm().PeriodStatus(1, at(5, 9, 0)); s != PeriodOutside {
		t.Fatalf("status = %s", s)
	}
	// Fora do horário não dá.
	if _, err := st.UseTimer("t", 1, at(5, 9, 0)); err == nil {
		t.Fatal("deveria recusar fora do horário")
	}

	start := at(5, 9, 0)
	u, err := st.UseTimer("t", 0, start)
	if err != nil {
		t.Fatal(err)
	}
	if !u.End.Equal(start.Add(10*time.Minute)) || u.Day != "2026-01-05" {
		t.Fatalf("use = %+v", u)
	}
	if got := st.ActiveSites(start.Add(5 * time.Minute)); len(got) != 0 {
		t.Fatalf("deveria estar desbloqueado: %v", got)
	}
	if s, end := tm().PeriodStatus(0, start.Add(5*time.Minute)); s != PeriodInUse || !end.Equal(u.End) {
		t.Fatalf("status = %s %v", s, end)
	}
	// Não dá para repetir nem durante nem depois.
	if _, err := st.UseTimer("t", 0, start.Add(5*time.Minute)); err == nil {
		t.Fatal("deveria recusar durante o uso")
	}
	after := start.Add(10 * time.Minute)
	if got := st.ActiveSites(after); len(got) != 1 {
		t.Fatal("deveria voltar a bloquear sozinho")
	}
	if s, _ := tm().PeriodStatus(0, after); s != PeriodUsed {
		t.Fatalf("status = %s", s)
	}
	if _, err := st.UseTimer("t", 0, at(5, 11, 0)); err == nil {
		t.Fatal("deveria recusar segundo uso no mesmo dia")
	}
	// O período da noite é independente.
	if _, err := st.UseTimer("t", 1, at(5, 18, 30)); err != nil {
		t.Fatal(err)
	}
	// No dia seguinte volta a estar disponível.
	if s, _ := tm().PeriodStatus(0, at(6, 6, 0)); s != PeriodAvailable {
		t.Fatalf("dia seguinte: status = %s", s)
	}
}

func TestTimerUseUsesPinnedTimezone(t *testing.T) {
	st := &State{Timers: []Timer{twoPeriods()}}
	// 09:00 UTC == 06:00 em São Paulo: dentro da manhã.
	if _, err := st.UseTimer("t", 0, time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	// 08:59 UTC == 05:59 em São Paulo: fora.
	st = &State{Timers: []Timer{twoPeriods()}}
	if _, err := st.UseTimer("t", 0, time.Date(2026, 1, 5, 8, 59, 0, 0, time.UTC)); err == nil {
		t.Fatal("deveria usar o fuso gravado")
	}
}

func TestTimerRemovalDelay(t *testing.T) {
	st := &State{Timers: []Timer{twoPeriods()}}
	now := at(5, 9, 0)
	removeAt, err := st.RequestRemoval("t", now)
	if err != nil || !removeAt.Equal(now.Add(RemovalDelay)) {
		t.Fatalf("removeAt = %v, %v", removeAt, err)
	}
	if again, _ := st.RequestRemoval("t", now.Add(time.Hour)); !again.Equal(removeAt) {
		t.Fatal("segundo pedido mudou o prazo")
	}
	if len(st.ActiveSites(now.Add(23*time.Hour))) != 1 || st.Prune(now.Add(23*time.Hour)) {
		t.Fatal("deveria continuar a bloquear durante 24h")
	}
	if !st.Prune(removeAt) || len(st.Timers) != 0 || st.Guarded(removeAt) {
		t.Fatal("deveria ser removido depois de 24h")
	}
}

func TestTimerPruneOldUses(t *testing.T) {
	st := &State{Timers: []Timer{twoPeriods()}}
	if _, err := st.UseTimer("t", 0, at(5, 9, 0)); err != nil {
		t.Fatal(err)
	}
	if st.Prune(at(6, 9, 0)) || len(st.Timers[0].Uses) != 1 {
		t.Fatal("pausa recente não pode sumir")
	}
	if !st.Prune(at(8, 9, 0)) || len(st.Timers[0].Uses) != 0 {
		t.Fatal("pausa antiga deveria ser descartada")
	}
}

func TestTimerMergeKeepsUses(t *testing.T) {
	used := &State{Timers: []Timer{twoPeriods()}}
	u, err := used.UseTimer("t", 0, at(5, 9, 0))
	if err != nil {
		t.Fatal(err)
	}
	remove := at(5, 9, 0)
	forged := twoPeriods()
	forged.RemoveAfter = &remove
	// Um arquivo sem a pausa e com remoção forjada; outro com a pausa.
	got := Merge(&State{Timers: []Timer{forged}}, used)
	if len(got.Timers) != 1 || got.Timers[0].RemoveAfter != nil {
		t.Fatalf("remoção forjada prevaleceu: %+v", got.Timers)
	}
	if len(got.Timers[0].Uses) != 1 || !got.Timers[0].Uses[0].End.Equal(u.End) {
		t.Fatalf("pausa gasta perdeu-se: %+v", got.Timers[0].Uses)
	}
	// Alongar a pausa num arquivo não dá minutos extra.
	longer := used.Clone()
	longer.Timers[0].Uses[0].End = u.End.Add(time.Hour)
	got = Merge(longer, used)
	if !got.Timers[0].Uses[0].End.Equal(u.End) {
		t.Fatal("pausa alongada prevaleceu")
	}
}

func TestValidatePeriods(t *testing.T) {
	ok := []TimerPeriod{{From: 360, To: 720, Minutes: 10}, {From: 1080, To: 1440, Minutes: 10}}
	if err := ValidatePeriods(ok); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]TimerPeriod{
		nil,
		{{From: 720, To: 360, Minutes: 10}},
		{{From: 360, To: 720, Minutes: 0}},
		{{From: 360, To: 370, Minutes: 11}},
		{{From: 360, To: 720, Minutes: 10}, {From: 700, To: 800, Minutes: 10}},
	} {
		if err := ValidatePeriods(bad); err == nil {
			t.Errorf("deveria falhar: %+v", bad)
		}
	}
}

func TestTimerPauseOpensInternetBlock(t *testing.T) {
	// Horário: toda a internet das 09:00 às 18:00, exceto github.com.
	sc := workday()
	sc.Sites, sc.Allow = []string{"*"}, []string{"github.com"}
	tm := twoPeriods() // x.com, com pausa de 10 min de manhã
	st := &State{Schedules: []Schedule{sc}, Timers: []Timer{tm}}

	now := at(5, 10, 0)
	if got := st.ActiveAllow(now); !reflect.DeepEqual(got, []string{"github.com"}) {
		t.Fatalf("antes da pausa: allow = %v", got)
	}
	if _, err := st.UseTimer("t", 0, now); err != nil {
		t.Fatal(err)
	}
	during := now.Add(5 * time.Minute)
	if got := withoutUpload(st.ActiveAllow(during)); !reflect.DeepEqual(got, []string{"github.com", "x.com"}) {
		t.Fatalf("durante a pausa: allow = %v", got)
	}
	if got := st.ActiveSites(during); !reflect.DeepEqual(got, []string{"*"}) {
		t.Fatalf("durante a pausa: active = %v", got)
	}
	if got := st.ActiveAllow(now.Add(10 * time.Minute)); !reflect.DeepEqual(got, []string{"github.com"}) {
		t.Fatalf("depois da pausa: allow = %v", got)
	}

	// Um site bloqueado um a um por outra regra continua bloqueado.
	st.Locks = []Lock{{ID: "l", Sites: []string{"x.com"}, Start: now, End: now.Add(time.Hour)}}
	if got := withoutUpload(st.ActiveAllow(during)); !reflect.DeepEqual(got, []string{"github.com"}) {
		t.Fatalf("com bloqueio explícito: allow = %v", got)
	}

	// Fora de um bloqueio da internet a pausa não cria exceções.
	st = &State{Timers: []Timer{twoPeriods()}}
	_, _ = st.UseTimer("t", 0, now)
	if got := st.ActiveAllow(during); got != nil {
		t.Fatalf("sem bloqueio da internet: allow = %v", got)
	}
}

func TestInternetTimerPauseLiftsInternetBlock(t *testing.T) {
	sc := workday()
	sc.Sites = []string{"*"}
	tm := twoPeriods()
	tm.Sites = []string{"*"}
	block := Lock{ID: "l", Sites: []string{"y.com"}, Start: at(5, 0, 0), End: at(6, 0, 0)}
	st := &State{Schedules: []Schedule{sc}, Timers: []Timer{tm}, Locks: []Lock{block}}
	now := at(5, 10, 0)
	if _, err := st.UseTimer("t", 0, now); err != nil {
		t.Fatal(err)
	}
	if got := st.ActiveSites(now.Add(time.Minute)); !reflect.DeepEqual(got, []string{"y.com"}) {
		t.Fatalf("active = %v", got)
	}
	if got := st.ActiveSites(now.Add(10 * time.Minute)); !reflect.DeepEqual(got, []string{"*", "y.com"}) {
		t.Fatalf("depois da pausa: active = %v", got)
	}
}

func TestTimerPauseOpensUploadStorage(t *testing.T) {
	sc := workday()
	sc.Sites = []string{"*"}
	st := &State{Schedules: []Schedule{sc}, Timers: []Timer{twoPeriods()}}
	now := at(5, 10, 0)
	if got := st.ActiveAllow(now); len(got) != 0 {
		t.Fatalf("antes da pausa: %v", got)
	}
	if _, err := st.UseTimer("t", 0, now); err != nil {
		t.Fatal(err)
	}
	got := st.ActiveAllow(now.Add(time.Minute))
	for _, want := range []string{"x.com", "amazonaws.com", "r2.cloudflarestorage.com"} {
		if !slices.Contains(got, want) {
			t.Errorf("faltou %q em %v", want, got)
		}
	}
	if got := st.ActiveAllow(now.Add(10 * time.Minute)); len(got) != 0 {
		t.Fatalf("depois da pausa: %v", got)
	}
}

// withoutUpload tira os UploadHosts (abertos em qualquer pausa).
func withoutUpload(list []string) []string {
	var out []string
	for _, s := range list {
		if !sites.IsUploadHost(s) {
			out = append(out, s)
		}
	}
	return out
}
