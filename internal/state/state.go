// Package state guarda os bloqueios e agendamentos e decide o que está ativo.
package state

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pedroivy/focolock/internal/sites"
)

// RemovalDelay é quanto tempo um agendamento continua valendo depois de você
// pedir para removê-lo. É o que impede "desligar rapidinho" num momento de fraqueza.
const RemovalDelay = 24 * time.Hour

// Lock é um bloqueio único: de Start até End, sem como cancelar.
type Lock struct {
	ID    string    `json:"id"`
	Sites []string  `json:"sites"`
	Allow []string  `json:"allow,omitempty"` // com "*": sites que continuam acessíveis
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Schedule é um bloqueio recorrente (ex.: seg-sex das 09:00 às 18:00).
type Schedule struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Sites   []string       `json:"sites"`
	Allow   []string       `json:"allow,omitempty"` // com "*": sites que continuam acessíveis
	Days    []time.Weekday `json:"days"`
	From    int            `json:"from"` // minutos desde 00:00
	To      int            `json:"to"`   // minutos desde 00:00; To <= From atravessa a meia-noite
	TZ      string         `json:"tz"`   // fixado na criação: mudar o fuso do Mac não burla
	Created time.Time      `json:"created"`
	// RemoveAfter: pedido de remoção; o agendamento só deixa de valer depois
	// disso E fora de uma janela ativa.
	RemoveAfter *time.Time `json:"remove_after,omitempty"`
}

type State struct {
	Locks     []Lock     `json:"locks"`
	Schedules []Schedule `json:"schedules"`
	Timers    []Timer    `json:"timers,omitempty"`
	// Relógio confiável (ver pacote clock).
	ClockOffset time.Duration `json:"clock_offset"`
	LastTrusted time.Time     `json:"last_trusted"`
}

// Clone copia o estado para ser enviado sem segurar o mutex do daemon.
func (st *State) Clone() *State {
	out := *st
	out.Locks = append([]Lock(nil), st.Locks...)
	out.Schedules = append([]Schedule(nil), st.Schedules...)
	out.Timers = make([]Timer, len(st.Timers))
	for i, t := range st.Timers {
		t.Uses = append([]TimerUse(nil), t.Uses...)
		out.Timers[i] = t
	}
	return &out
}

func NewID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s Schedule) location() *time.Location {
	if loc, err := time.LoadLocation(s.TZ); err == nil {
		return loc
	}
	return time.Local
}

func (s Schedule) hasDay(d time.Weekday) bool {
	for _, x := range s.Days {
		if x == d {
			return true
		}
	}
	return false
}

// WindowActive diz se o horário cai numa janela do agendamento (ignorando remoção).
func (s Schedule) WindowActive(now time.Time) bool {
	t := now.In(s.location())
	m := t.Hour()*60 + t.Minute()
	today := t.Weekday()
	yesterday := (today + 6) % 7
	switch {
	case s.From == s.To: // dia inteiro
		return s.hasDay(today)
	case s.From < s.To:
		return s.hasDay(today) && m >= s.From && m < s.To
	default: // atravessa a meia-noite, ex. 22:00-06:00
		return (s.hasDay(today) && m >= s.From) || (s.hasDay(yesterday) && m < s.To)
	}
}

// Removed diz se o agendamento já pode ser descartado.
func (s Schedule) Removed(now time.Time) bool {
	return s.RemoveAfter != nil && !now.Before(*s.RemoveAfter) && !s.WindowActive(now)
}

func (s Schedule) Active(now time.Time) bool {
	return !s.Removed(now) && s.WindowActive(now)
}

func (l Lock) Active(now time.Time) bool {
	return !now.Before(l.Start) && now.Before(l.End)
}

// active devolve (sites, exceções) de cada coisa que está a bloquear agora.
func (st *State) active(now time.Time) (lists, allows [][]string) {
	for _, l := range st.Locks {
		if l.Active(now) {
			lists, allows = append(lists, l.Sites), append(allows, l.Allow)
		}
	}
	for _, sc := range st.Schedules {
		if sc.Active(now) {
			lists, allows = append(lists, sc.Sites), append(allows, sc.Allow)
		}
	}
	for _, t := range st.Timers {
		if t.Blocking(now) {
			lists, allows = append(lists, t.Sites), append(allows, t.Allow)
		}
	}
	return lists, allows
}

// ActiveSites devolve a união dos sites de tudo que está ativo agora.
func (st *State) ActiveSites(now time.Time) []string {
	lists, _ := st.active(now)
	set := map[string]bool{}
	for _, l := range lists {
		for _, s := range l {
			set[s] = true
		}
	}
	return sortedKeys(set)
}

// ActiveAllow devolve as exceções ao bloqueio de toda a internet. Se houver
// vários bloqueios de internet ativos, só passa o que todos permitem; e um
// site bloqueado explicitamente por outra regra nunca é exceção.
func (st *State) ActiveAllow(now time.Time) []string {
	lists, allows := st.active(now)
	var allow map[string]bool
	blocked := map[string]bool{}
	for i, l := range lists {
		if !contains(l, sites.Internet) {
			for _, s := range l {
				blocked[s] = true
			}
			continue
		}
		cur := map[string]bool{}
		for _, s := range allows[i] {
			if allow == nil || allow[s] {
				cur[s] = true
			}
		}
		allow = cur
	}
	for s := range blocked {
		delete(allow, s)
	}
	return sortedKeys(allow)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// intersect devolve só as exceções presentes nas duas cópias: acrescentar
// exceções editando um dos arquivos não adianta.
func intersect(a, b []string) []string {
	var out []string
	for _, s := range a {
		if contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}

// Guarded: enquanto houver bloqueio futuro/ativo ou agendamento, o app se
// protege contra desinstalação.
func (st *State) Guarded(now time.Time) bool {
	for _, l := range st.Locks {
		if now.Before(l.End) {
			return true
		}
	}
	for _, sc := range st.Schedules {
		if !sc.Removed(now) {
			return true
		}
	}
	for _, t := range st.Timers {
		if !t.Removed(now) {
			return true
		}
	}
	return false
}

// Prune descarta bloqueios terminados e agendamentos removidos.
func (st *State) Prune(now time.Time) bool {
	changed := false
	locks := st.Locks[:0]
	for _, l := range st.Locks {
		if now.Before(l.End) {
			locks = append(locks, l)
		} else {
			changed = true
		}
	}
	st.Locks = locks
	scheds := st.Schedules[:0]
	for _, sc := range st.Schedules {
		if !sc.Removed(now) {
			scheds = append(scheds, sc)
		} else {
			changed = true
		}
	}
	st.Schedules = scheds
	timers := st.Timers[:0]
	for _, t := range st.Timers {
		if t.Removed(now) {
			changed = true
			continue
		}
		if t.pruneUses(now) {
			changed = true
		}
		timers = append(timers, t)
	}
	st.Timers = timers
	return changed
}

// RequestRemoval agenda a remoção de um agendamento ou temporizador para daqui
// a RemovalDelay.
func (st *State) RequestRemoval(id string, now time.Time) (time.Time, error) {
	for i := range st.Schedules {
		sc := &st.Schedules[i]
		if sc.ID != id {
			continue
		}
		if sc.RemoveAfter != nil {
			return *sc.RemoveAfter, nil
		}
		at := now.Add(RemovalDelay)
		sc.RemoveAfter = &at
		return at, nil
	}
	for i := range st.Timers {
		t := &st.Timers[i]
		if t.ID != id {
			continue
		}
		if t.RemoveAfter != nil {
			return *t.RemoveAfter, nil
		}
		at := now.Add(RemovalDelay)
		t.RemoveAfter = &at
		return at, nil
	}
	return time.Time{}, fmt.Errorf("agendamento ou temporizador %q não encontrado", id)
}

// Merge une dois estados (arquivo principal + backup). Ninguém consegue
// "apagar" um bloqueio editando só um dos arquivos.
func Merge(a, b *State) *State {
	out := &State{}
	locks := map[string]Lock{}
	for _, l := range append(append([]Lock{}, a.Locks...), b.Locks...) {
		cur, ok := locks[l.ID]
		if !ok {
			locks[l.ID] = l
			continue
		}
		allow := intersect(cur.Allow, l.Allow)
		if l.End.After(cur.End) {
			cur = l
		}
		cur.Allow = allow
		locks[l.ID] = cur
	}
	for _, l := range locks {
		out.Locks = append(out.Locks, l)
	}
	sort.Slice(out.Locks, func(i, j int) bool { return out.Locks[i].Start.Before(out.Locks[j].Start) })

	scheds := map[string]Schedule{}
	for _, s := range append(append([]Schedule{}, a.Schedules...), b.Schedules...) {
		cur, ok := scheds[s.ID]
		if !ok {
			scheds[s.ID] = s
			continue
		}
		allow := intersect(cur.Allow, s.Allow)
		// Prevalece o pedido de remoção mais tardio (ou nenhum).
		if cur.RemoveAfter != nil && (s.RemoveAfter == nil || s.RemoveAfter.After(*cur.RemoveAfter)) {
			cur = s
		}
		cur.Allow = allow
		scheds[s.ID] = cur
	}
	for _, s := range scheds {
		out.Schedules = append(out.Schedules, s)
	}
	sort.Slice(out.Schedules, func(i, j int) bool { return out.Schedules[i].Created.Before(out.Schedules[j].Created) })

	out.Timers = mergeTimers(a.Timers, b.Timers)

	out.ClockOffset, out.LastTrusted = a.ClockOffset, a.LastTrusted
	if b.LastTrusted.After(a.LastTrusted) {
		out.ClockOffset, out.LastTrusted = b.ClockOffset, b.LastTrusted
	}
	return out
}

// ParseHHMM converte "09:30" em minutos desde a meia-noite.
func ParseHHMM(s string) (int, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("horário inválido %q (use HH:MM)", s)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("horário inválido %q (use HH:MM)", s)
	}
	return h*60 + m, nil
}

func FormatHHMM(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

var dayNames = map[string]time.Weekday{
	"dom": time.Sunday, "sun": time.Sunday,
	"seg": time.Monday, "mon": time.Monday,
	"ter": time.Tuesday, "tue": time.Tuesday,
	"qua": time.Wednesday, "wed": time.Wednesday,
	"qui": time.Thursday, "thu": time.Thursday,
	"sex": time.Friday, "fri": time.Friday,
	"sab": time.Saturday, "sáb": time.Saturday, "sat": time.Saturday,
}

// ParseDays aceita "seg-sex", "sab,dom", "todos", "mon-fri", "seg,qua,sex".
func ParseDays(s string) ([]time.Weekday, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "todos" || s == "all" || s == "*" {
		return []time.Weekday{0, 1, 2, 3, 4, 5, 6}, nil
	}
	set := map[time.Weekday]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if a, b, ok := strings.Cut(part, "-"); ok {
			from, ok1 := dayNames[a]
			to, ok2 := dayNames[b]
			if !ok1 || !ok2 {
				return nil, fmt.Errorf("dias inválidos %q", part)
			}
			for d := from; ; d = (d + 1) % 7 {
				set[d] = true
				if d == to {
					break
				}
			}
			continue
		}
		d, ok := dayNames[part]
		if !ok {
			return nil, fmt.Errorf("dia inválido %q (use dom,seg,ter,qua,qui,sex,sab)", part)
		}
		set[d] = true
	}
	var out []time.Weekday
	for d := time.Sunday; d <= time.Saturday; d++ {
		if set[d] {
			out = append(out, d)
		}
	}
	return out, nil
}

func FormatDays(days []time.Weekday) string {
	names := []string{"dom", "seg", "ter", "qua", "qui", "sex", "sab"}
	var parts []string
	for _, d := range days {
		parts = append(parts, names[d])
	}
	return strings.Join(parts, ",")
}
