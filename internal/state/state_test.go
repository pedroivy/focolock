package state

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

var sp, _ = time.LoadLocation("America/Sao_Paulo")

func at(day, h, m int) time.Time { // jan/2026: dia 5 é segunda
	return time.Date(2026, 1, day, h, m, 0, 0, sp)
}

func workday() Schedule {
	return Schedule{ID: "a", Sites: []string{"x.com"}, Days: []time.Weekday{1, 2, 3, 4, 5}, From: 9 * 60, To: 18 * 60, TZ: "America/Sao_Paulo"}
}

func TestScheduleWindow(t *testing.T) {
	s := workday()
	cases := []struct {
		t    time.Time
		want bool
	}{
		{at(5, 8, 59), false},
		{at(5, 9, 0), true},
		{at(5, 17, 59), true},
		{at(5, 18, 0), false},
		{at(10, 12, 0), false}, // sábado
	}
	for _, c := range cases {
		if got := s.WindowActive(c.t); got != c.want {
			t.Errorf("%v: got %v want %v", c.t, got, c.want)
		}
	}
}

func TestScheduleOvernight(t *testing.T) {
	// Sexta 22:00 até sábado 06:00.
	s := Schedule{Days: []time.Weekday{time.Friday}, From: 22 * 60, To: 6 * 60, TZ: "America/Sao_Paulo"}
	if !s.WindowActive(at(9, 23, 0)) || !s.WindowActive(at(10, 5, 59)) {
		t.Fatal("deveria estar ativo sexta 23h e sábado 5:59")
	}
	if s.WindowActive(at(10, 6, 0)) || s.WindowActive(at(8, 23, 0)) {
		t.Fatal("não deveria estar ativo sábado 6:00 nem quinta 23h")
	}
}

func TestTimezoneIsPinned(t *testing.T) {
	s := workday()
	// 10:00 em São Paulo == 13:00 UTC. Mudar o fuso do Mac não muda a janela.
	if !s.WindowActive(time.Date(2026, 1, 5, 13, 0, 0, 0, time.UTC)) {
		t.Fatal("janela deveria usar o fuso salvo")
	}
}

func TestRemovalDelay(t *testing.T) {
	st := &State{Schedules: []Schedule{workday()}}
	now := at(5, 10, 0) // dentro da janela
	removeAt, err := st.RequestRemoval("a", now)
	if err != nil {
		t.Fatal(err)
	}
	if !removeAt.Equal(now.Add(RemovalDelay)) {
		t.Fatalf("removeAt = %v", removeAt)
	}
	// Pedir de novo não adianta o prazo.
	again, _ := st.RequestRemoval("a", now.Add(time.Hour))
	if !again.Equal(removeAt) {
		t.Fatal("segundo pedido mudou o prazo")
	}
	if !st.Schedules[0].Active(now.Add(time.Hour)) {
		t.Fatal("deveria continuar ativo logo após o pedido")
	}
	// 24h depois (terça 10:00) está numa janela: continua até 18:00.
	if !st.Schedules[0].Active(at(6, 10, 0)) {
		t.Fatal("não pode sumir no meio de uma janela")
	}
	if st.Prune(at(6, 17, 0)) {
		t.Fatal("prune no meio da janela")
	}
	if !st.Prune(at(6, 18, 0)) || len(st.Schedules) != 0 {
		t.Fatal("deveria ser removido ao fim da janela")
	}
}

func TestLocksAndGuarded(t *testing.T) {
	now := at(5, 10, 0)
	st := &State{Locks: []Lock{{ID: "l", Sites: []string{"a.com"}, Start: now, End: now.Add(time.Hour)}}}
	if got := st.ActiveSites(now); !reflect.DeepEqual(got, []string{"a.com"}) {
		t.Fatalf("active = %v", got)
	}
	if !st.Guarded(now) {
		t.Fatal("deveria estar protegido")
	}
	st.Prune(now.Add(time.Hour))
	if st.Guarded(now.Add(time.Hour)) || len(st.Locks) != 0 {
		t.Fatal("bloqueio terminado deveria sumir")
	}
}

func TestMergeCannotDeleteViaOneFile(t *testing.T) {
	now := at(5, 10, 0)
	l := Lock{ID: "l", Sites: []string{"a.com"}, Start: now, End: now.Add(time.Hour)}
	shortened := l
	shortened.End = now.Add(time.Minute)
	remove := now
	sc := workday()
	scRemoved := sc
	scRemoved.RemoveAfter = &remove

	got := Merge(&State{Locks: []Lock{shortened}, Schedules: []Schedule{scRemoved}}, &State{Locks: []Lock{l}, Schedules: []Schedule{sc}})
	if len(got.Locks) != 1 || !got.Locks[0].End.Equal(l.End) {
		t.Fatalf("lock encurtado prevaleceu: %+v", got.Locks)
	}
	if got.Schedules[0].RemoveAfter != nil {
		t.Fatal("remoção forjada prevaleceu")
	}
}

func TestStoreRoundTripAndBackup(t *testing.T) {
	dir := t.TempDir()
	s := Store{Primary: filepath.Join(dir, "a", "state.json"), Backup: filepath.Join(dir, "b", "state.json")}
	st := &State{Schedules: []Schedule{workday()}}
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(s.Primary, []byte("lixo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.Load(); len(got.Schedules) != 1 {
		t.Fatalf("backup não recuperou: %+v", got)
	}
}

func TestParseDays(t *testing.T) {
	cases := map[string][]time.Weekday{
		"seg-sex": {1, 2, 3, 4, 5},
		"sab,dom": {0, 6},
		"sex-seg": {0, 1, 5, 6},
		"todos":   {0, 1, 2, 3, 4, 5, 6},
		"mon,wed": {1, 3},
	}
	for in, want := range cases {
		got, err := ParseDays(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseDays(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseDays("feriado"); err == nil {
		t.Error("deveria falhar")
	}
}

func TestActiveAllow(t *testing.T) {
	now := at(5, 10, 0)
	lock := func(id string, sites, allow []string) Lock {
		return Lock{ID: id, Sites: sites, Allow: allow, Start: now, End: now.Add(time.Hour)}
	}
	st := &State{Locks: []Lock{lock("a", []string{"*"}, []string{"github.com", "wikipedia.org", "x.com"})}}
	if got := st.ActiveAllow(now); !reflect.DeepEqual(got, []string{"github.com", "wikipedia.org", "x.com"}) {
		t.Fatalf("allow = %v", got)
	}
	// Um site bloqueado por outra regra nunca é exceção.
	st.Locks = append(st.Locks, lock("b", []string{"x.com"}, nil))
	// Dois bloqueios da internet: só passa o que ambos permitem.
	st.Locks = append(st.Locks, lock("c", []string{"*"}, []string{"wikipedia.org", "x.com"}))
	if got := st.ActiveAllow(now); !reflect.DeepEqual(got, []string{"wikipedia.org"}) {
		t.Fatalf("allow = %v", got)
	}
	// Sem bloqueio da internet não há exceções.
	if got := (&State{Locks: []Lock{lock("b", []string{"x.com"}, nil)}}).ActiveAllow(now); len(got) != 0 {
		t.Fatalf("allow = %v", got)
	}
	// Bloqueio da internet sem exceções anula as de outro.
	st = &State{Locks: []Lock{lock("a", []string{"*"}, []string{"github.com"}), lock("c", []string{"*"}, nil)}}
	if got := st.ActiveAllow(now); len(got) != 0 {
		t.Fatalf("allow = %v", got)
	}
}

func TestMergeCannotAddExceptions(t *testing.T) {
	now := at(5, 10, 0)
	l := Lock{ID: "l", Sites: []string{"*"}, Allow: []string{"github.com"}, Start: now, End: now.Add(time.Hour)}
	forged := l
	forged.Allow = []string{"github.com", "youtube.com"}
	sc := workday()
	sc.Sites, sc.Allow = []string{"*"}, nil
	scForged := sc
	scForged.Allow = []string{"youtube.com"}
	tm := twoPeriods()
	tm.Sites = []string{"*"}
	tmForged := tm
	tmForged.Allow = []string{"youtube.com"}
	got := Merge(&State{Locks: []Lock{forged}, Schedules: []Schedule{scForged}, Timers: []Timer{tmForged}},
		&State{Locks: []Lock{l}, Schedules: []Schedule{sc}, Timers: []Timer{tm}})
	if !reflect.DeepEqual(got.Locks[0].Allow, []string{"github.com"}) || len(got.Schedules[0].Allow) != 0 || len(got.Timers[0].Allow) != 0 {
		t.Fatalf("exceção forjada prevaleceu: %+v %+v %+v", got.Locks[0].Allow, got.Schedules[0].Allow, got.Timers[0].Allow)
	}
}
