package state

import (
	"fmt"
	"sort"
	"time"
)

// Timer (Temporizador): os sites ficam sempre bloqueados, exceto durante uma
// "pausa" de N minutos que pode ser gasta uma vez por dia em cada período.
type Timer struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Sites   []string      `json:"sites"`
	Allow   []string      `json:"allow,omitempty"` // com "*": sites que continuam acessíveis
	Periods []TimerPeriod `json:"periods"`
	TZ      string        `json:"tz"` // fixado na criação, como nos agendamentos
	Created time.Time     `json:"created"`
	// Uses: pausas já gastas. Servem para saber o que já foi usado hoje.
	Uses        []TimerUse `json:"uses,omitempty"`
	RemoveAfter *time.Time `json:"remove_after,omitempty"`
}

// TimerPeriod: entre From e To (minutos desde 00:00, From < To) pode-se usar
// Minutes minutos de acesso, uma vez por dia.
type TimerPeriod struct {
	From    int `json:"from"`
	To      int `json:"to"`
	Minutes int `json:"minutes"`
}

// TimerUse é uma pausa gasta: período Period no dia Day (AAAA-MM-DD, no fuso
// do temporizador), desbloqueado de Start até End (hora confiável).
type TimerUse struct {
	Period int       `json:"period"`
	Day    string    `json:"day"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
}

// Estados de um período, do ponto de vista de quem usa a app.
const (
	PeriodAvailable = "available" // dentro do horário e ainda não usado hoje
	PeriodInUse     = "in_use"    // desbloqueado agora
	PeriodUsed      = "used"      // já usado hoje
	PeriodOutside   = "outside"   // fora do horário
)

// usesKept: pausas mais antigas que isto já não interessam e são descartadas.
const usesKept = 48 * time.Hour

func (t Timer) location() *time.Location {
	if loc, err := time.LoadLocation(t.TZ); err == nil {
		return loc
	}
	return time.Local
}

// dayKey e minuto do dia de now no fuso do temporizador.
func (t Timer) dayAndMinute(now time.Time) (string, int) {
	lt := now.In(t.location())
	return lt.Format("2006-01-02"), lt.Hour()*60 + lt.Minute()
}

// ValidatePeriods confere que os períodos fazem sentido e não se sobrepõem.
func ValidatePeriods(ps []TimerPeriod) error {
	if len(ps) == 0 {
		return fmt.Errorf("defina pelo menos um período")
	}
	sorted := append([]TimerPeriod(nil), ps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].From < sorted[j].From })
	for i, p := range sorted {
		if p.From < 0 || p.To > 24*60 || p.From >= p.To {
			return fmt.Errorf("período %s–%s inválido (o fim tem de ser depois do início, no mesmo dia)", FormatHHMM(p.From), FormatHHMM(p.To))
		}
		if p.Minutes < 1 || p.Minutes > p.To-p.From {
			return fmt.Errorf("período %s–%s: os minutos têm de estar entre 1 e %d", FormatHHMM(p.From), FormatHHMM(p.To), p.To-p.From)
		}
		if i > 0 && p.From < sorted[i-1].To {
			return fmt.Errorf("os períodos %s–%s e %s–%s sobrepõem-se",
				FormatHHMM(sorted[i-1].From), FormatHHMM(sorted[i-1].To), FormatHHMM(p.From), FormatHHMM(p.To))
		}
	}
	return nil
}

// Removed diz se o temporizador já pode ser descartado.
func (t Timer) Removed(now time.Time) bool {
	return t.RemoveAfter != nil && !now.Before(*t.RemoveAfter)
}

// Unlocked diz se há uma pausa a decorrer.
func (t Timer) Unlocked(now time.Time) bool {
	for _, u := range t.Uses {
		if !now.Before(u.Start) && now.Before(u.End) {
			return true
		}
	}
	return false
}

// Blocking diz se os sites do temporizador estão bloqueados agora.
func (t Timer) Blocking(now time.Time) bool {
	return !t.Removed(now) && !t.Unlocked(now)
}

// PeriodStatus devolve o estado do período i e, se estiver a ser usado, quando acaba.
func (t Timer) PeriodStatus(i int, now time.Time) (string, time.Time) {
	day, m := t.dayAndMinute(now)
	used := false
	for _, u := range t.Uses {
		if u.Period != i {
			continue
		}
		if !now.Before(u.Start) && now.Before(u.End) {
			return PeriodInUse, u.End
		}
		if u.Day == day {
			used = true
		}
	}
	switch {
	case used:
		return PeriodUsed, time.Time{}
	case i >= 0 && i < len(t.Periods) && m >= t.Periods[i].From && m < t.Periods[i].To:
		return PeriodAvailable, time.Time{}
	}
	return PeriodOutside, time.Time{}
}

// EmergencyPeriod marca, em Uses, a pausa de emergência (não é de nenhum período).
const EmergencyPeriod = -1

// UseEmergency gasta a pausa de emergência num temporizador: abre os sites
// dele durante os minutos do seu primeiro período, a qualquer hora. Só existe
// uma, para sempre, em todo o estado.
func (st *State) UseEmergency(id string, now time.Time) (TimerUse, error) {
	if st.EmergencyUsed != nil {
		return TimerUse{}, fmt.Errorf("a pausa de emergência já foi usada (%s)", st.EmergencyUsed.Format("02/01/2006 15:04"))
	}
	for i := range st.Timers {
		t := &st.Timers[i]
		if t.ID != id {
			continue
		}
		if t.Removed(now) || len(t.Periods) == 0 {
			return TimerUse{}, fmt.Errorf("temporizador %q não está ativo", id)
		}
		day, _ := t.dayAndMinute(now)
		u := TimerUse{Period: EmergencyPeriod, Day: day, Start: now, End: now.Add(time.Duration(t.Periods[0].Minutes) * time.Minute)}
		t.Uses = append(t.Uses, u)
		used := now
		st.EmergencyUsed = &used
		return u, nil
	}
	return TimerUse{}, fmt.Errorf("temporizador %q não encontrado", id)
}

// UseTimer gasta a pausa do período idx do temporizador id. Só funciona dentro
// do horário do período e uma vez por dia; não há como pausar nem repetir.
func (st *State) UseTimer(id string, idx int, now time.Time) (TimerUse, error) {
	for i := range st.Timers {
		t := &st.Timers[i]
		if t.ID != id {
			continue
		}
		if t.Removed(now) {
			return TimerUse{}, fmt.Errorf("temporizador %q já foi removido", id)
		}
		if idx < 0 || idx >= len(t.Periods) {
			return TimerUse{}, fmt.Errorf("período %d não existe", idx)
		}
		p := t.Periods[idx]
		switch s, _ := t.PeriodStatus(idx, now); s {
		case PeriodInUse:
			return TimerUse{}, fmt.Errorf("este período já está a ser usado")
		case PeriodUsed:
			return TimerUse{}, fmt.Errorf("o período %s–%s já foi usado hoje", FormatHHMM(p.From), FormatHHMM(p.To))
		case PeriodOutside:
			return TimerUse{}, fmt.Errorf("fora do horário: este período é das %s às %s", FormatHHMM(p.From), FormatHHMM(p.To))
		}
		day, _ := t.dayAndMinute(now)
		u := TimerUse{Period: idx, Day: day, Start: now, End: now.Add(time.Duration(p.Minutes) * time.Minute)}
		t.Uses = append(t.Uses, u)
		return u, nil
	}
	return TimerUse{}, fmt.Errorf("temporizador %q não encontrado", id)
}

// pruneUses descarta pausas antigas. Devolve true se mudou algo.
func (t *Timer) pruneUses(now time.Time) bool {
	uses := t.Uses[:0]
	for _, u := range t.Uses {
		if now.Sub(u.End) < usesKept {
			uses = append(uses, u)
		}
	}
	changed := len(uses) != len(t.Uses)
	t.Uses = uses
	return changed
}

// mergeTimers une os temporizadores dos dois arquivos. As pausas gastas são
// unidas (apagar uma num arquivo não devolve os minutos) e, para o mesmo
// período e dia, prevalece a que acaba mais cedo.
func mergeTimers(a, b []Timer) []Timer {
	byID := map[string]Timer{}
	var order []string
	for _, t := range append(append([]Timer{}, a...), b...) {
		cur, ok := byID[t.ID]
		if !ok {
			t.Uses = append([]TimerUse(nil), t.Uses...)
			byID[t.ID] = t
			order = append(order, t.ID)
			continue
		}
		uses, allow := cur.Uses, intersect(cur.Allow, t.Allow)
		// Prevalece o pedido de remoção mais tardio (ou nenhum).
		if cur.RemoveAfter != nil && (t.RemoveAfter == nil || t.RemoveAfter.After(*cur.RemoveAfter)) {
			cur = t
		}
		cur.Uses = mergeUses(uses, t.Uses)
		cur.Allow = allow
		byID[t.ID] = cur
	}
	out := make([]Timer, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

func mergeUses(a, b []TimerUse) []TimerUse {
	type key struct {
		p int
		d string
	}
	m := map[key]TimerUse{}
	for _, u := range append(append([]TimerUse{}, a...), b...) {
		k := key{u.Period, u.Day}
		if cur, ok := m[k]; !ok || u.End.Before(cur.End) {
			m[k] = u
		}
	}
	out := make([]TimerUse, 0, len(m))
	for _, u := range m {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}
