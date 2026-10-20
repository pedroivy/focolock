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
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Sites   []string        `json:"sites"`
	Allow   []string        `json:"allow,omitempty"` // com "*": sites que continuam acessíveis
	Days    []time.Weekday  `json:"days"`
	From    int             `json:"from"` // minutos desde 00:00
	To      int             `json:"to"`   // minutos desde 00:00; To <= From atravessa a meia-noite
	TZ      string          `json:"tz"`   // fixado na criação: mudar o fuso do Mac não burla
	Created time.Time       `json:"created"`
	Delays  []ScheduleDelay `json:"delays,omitempty"`
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
	out.Schedules = make([]Schedule, len(st.Schedules))
	for i, s := range st.Schedules {
		s.Delays = append([]ScheduleDelay(nil), s.Delays...)
		out.Schedules[i] = s
	}
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

// MaxDelay: quanto se pode adiar a janela de um dia (ver Delay).
const MaxDelay = 60

// ScheduleDelay adia a janela que começa no dia Day (AAAA-MM-DD, no fuso do
// agendamento) Minutes minutos. A janela é empurrada, não encolhe.
type ScheduleDelay struct {
	Day     string `json:"day"`
	Minutes int    `json:"minutes"`
}

func (s Schedule) delay(day string) int {
	for _, d := range s.Delays {
		if d.Day == day {
			return d.Minutes
		}
	}
	return 0
}

// Window devolve a janela que começa no dia de t (no fuso do agendamento),
// já com o adiamento desse dia. ok é false se o dia não tem janela.
func (s Schedule) Window(t time.Time) (start, end time.Time, ok bool) {
	lt := t.In(s.location())
	if !s.hasDay(lt.Weekday()) {
		return start, end, false
	}
	y, m, d := lt.Date()
	at := func(day, minutes int) time.Time {
		return time.Date(y, m, d+day, minutes/60, minutes%60, 0, 0, lt.Location())
	}
	switch {
	case s.From == s.To: // dia inteiro
		start, end = at(0, 0), at(1, 0)
	case s.From < s.To:
		start, end = at(0, s.From), at(0, s.To)
	default: // atravessa a meia-noite, ex. 22:00-06:00
		start, end = at(0, s.From), at(1, s.To)
	}
	shift := time.Duration(s.delay(lt.Format("2006-01-02"))) * time.Minute
	return start.Add(shift), end.Add(shift), true
}

// WindowActive diz se o horário cai numa janela do agendamento (ignorando remoção).
func (s Schedule) WindowActive(now time.Time) bool {
	lt := now.In(s.location())
	// A janela ativa começou hoje ou ontem (se atravessa a meia-noite ou foi adiada).
	for _, day := range []time.Time{lt, lt.AddDate(0, 0, -1)} {
		if start, end, ok := s.Window(day); ok && !now.Before(start) && now.Before(end) {
			return true
		}
	}
	return false
}

// Delay adia a janela de hoje do agendamento id. Só antes de ela começar,
// no máximo MaxDelay minutos e uma vez por dia: não serve para sair de um
// bloqueio a meio.
func (st *State) Delay(id string, minutes int, now time.Time) (time.Time, time.Time, error) {
	var zero time.Time
	if minutes < 1 || minutes > MaxDelay {
		return zero, zero, fmt.Errorf("só dá para adiar entre 1 e %d minutos", MaxDelay)
	}
	for i := range st.Schedules {
		s := &st.Schedules[i]
		if s.ID != id {
			continue
		}
		if s.Removed(now) {
			return zero, zero, fmt.Errorf("agendamento %q já foi removido", id)
		}
		day := now.In(s.location()).Format("2006-01-02")
		if s.delay(day) > 0 {
			return zero, zero, fmt.Errorf("este horário já foi adiado hoje")
		}
		start, _, ok := s.Window(now)
		if !ok {
			return zero, zero, fmt.Errorf("este horário não tem bloqueio hoje")
		}
		if !now.Before(start) || s.WindowActive(now) {
			return zero, zero, fmt.Errorf("só dá para adiar antes de o bloqueio começar")
		}
		s.Delays = append(s.Delays, ScheduleDelay{Day: day, Minutes: minutes})
		start, end, _ := s.Window(now)
		return start, end, nil
	}
	return zero, zero, fmt.Errorf("agendamento %q não encontrado", id)
}

// pruneDelays descarta adiamentos com mais de dois dias.
func (s *Schedule) pruneDelays(now time.Time) bool {
	cutoff := now.In(s.location()).AddDate(0, 0, -2).Format("2006-01-02")
	out := s.Delays[:0]
	for _, d := range s.Delays {
		if d.Day >= cutoff {
			out = append(out, d)
		}
	}
	changed := len(out) != len(s.Delays)
	s.Delays = out
	return changed
}

// intersectDelays: só valem adiamentos que estão nas duas cópias do estado.
func intersectDelays(a, b []ScheduleDelay) []ScheduleDelay {
	var out []ScheduleDelay
	for _, x := range a {
		for _, y := range b {
			if x == y {
				out = append(out, x)
				break
			}
		}
	}
	return out
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
//
// Uma pausa de temporizador a decorrer abre os sites desse temporizador
// também por cima do bloqueio de toda a internet (ver ActiveAllow). Se o
// temporizador for da própria internet, a pausa levanta o bloqueio da
// internet; sites bloqueados um a um continuam bloqueados.
func (st *State) ActiveSites(now time.Time) []string {
	lists, _ := st.active(now)
	set := map[string]bool{}
	for _, l := range lists {
		for _, s := range l {
			set[s] = true
		}
	}
	if st.pausedSites(now)[sites.Internet] {
		delete(set, sites.Internet)
	}
	return sortedKeys(set)
}

// ActiveAllow devolve as exceções ao bloqueio de toda a internet. Se houver
// vários bloqueios de internet ativos, só passa o que todos permitem. Os
// sites de um temporizador em pausa também passam. Um site bloqueado
// explicitamente por outra regra nunca é exceção.
func (st *State) ActiveAllow(now time.Time) []string {
	lists, allows := st.active(now)
	var allow map[string]bool
	blocked := map[string]bool{}
	for i, l := range lists {
		for _, s := range l {
			if s != sites.Internet {
				blocked[s] = true
			}
		}
		if !contains(l, sites.Internet) {
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
	if allow == nil {
		return nil // sem bloqueio da internet não há exceções
	}
	for s := range st.pausedSites(now) {
		if s != sites.Internet {
			allow[s] = true
		}
	}
	for s := range blocked {
		delete(allow, s)
	}
	return sortedKeys(allow)
}

// pausedSites: sites dos temporizadores com uma pausa a decorrer agora.
func (st *State) pausedSites(now time.Time) map[string]bool {
	out := map[string]bool{}
	for _, t := range st.Timers {
		if !t.Removed(now) && t.Unlocked(now) {
			for _, s := range t.Sites {
				out[s] = true
			}
		}
	}
	return out
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
			if sc.pruneDelays(now) {
				changed = true
			}
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
		allow, delays := intersect(cur.Allow, s.Allow), intersectDelays(cur.Delays, s.Delays)
		// Prevalece o pedido de remoção mais tardio (ou nenhum).
		if cur.RemoveAfter != nil && (s.RemoveAfter == nil || s.RemoveAfter.After(*cur.RemoveAfter)) {
			cur = s
		}
		cur.Allow, cur.Delays = allow, delays
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
