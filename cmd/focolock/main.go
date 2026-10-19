// focolock: bloqueador de sites para macOS no estilo Cold Turkey.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/pedroivy/focolock/internal/api"
	"github.com/pedroivy/focolock/internal/daemon"
	"github.com/pedroivy/focolock/internal/enforce"
	"github.com/pedroivy/focolock/internal/guard"
	"github.com/pedroivy/focolock/internal/state"
)

const usage = `focolock — bloqueador de sites que não dá pra burlar fechando o app

Uso:
  focolock bloquear --por 2h youtube.com x.com      bloqueia agora, por um tempo
  focolock bloquear --ate 18:00 instagram.com       bloqueia agora, até um horário
  focolock bloquear --por 2h --exceto wikipedia.org,github.com internet
                                                    toda a internet, menos alguns sites
  focolock agendar --dias seg-sex --das 09:00 --ate 18:00 [--nome trabalho] youtube.com
  focolock temporizador --periodos 06:00-12:00=10,18:00-23:59=10 [--nome redes] x.com
                                                    sempre bloqueado, com N minutos livres por período
  focolock usar <id> <período>                      gasta os minutos de um período (1, 2, …)
  focolock status                                   mostra o que está bloqueado
  focolock abrir                                    abre a janela do app
  focolock remover <id>                             remove um agendamento ou temporizador (após 24h)

  sudo focolock instalar                            instala/atualiza o serviço
  sudo focolock desinstalar                         só funciona sem bloqueios ativos

Sites: "internet" bloqueia toda a internet; --exceto lista os sites que continuam
a funcionar (vale em bloquear, agendar e temporizador).
Dias: dom seg ter qua qui sex sab, intervalos (seg-sex), listas (sab,dom) ou "todos".
Bloqueios não podem ser cancelados nem encurtados. Pense antes de confirmar.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "bloquear", "lock":
		err = cmdLock(args)
	case "agendar", "schedule":
		err = cmdSchedule(args)
	case "temporizador", "timer":
		err = cmdTimer(args)
	case "usar", "use":
		err = cmdTimerUse(args)
	case "status", "st":
		err = cmdStatus()
	case "abrir", "open", "app":
		err = exec.Command("/usr/bin/open", "/Applications/Focolock.app").Run()
	case "remover", "rm":
		err = cmdRemove(args)
	case "instalar", "install":
		if err = guard.Install(); err == nil {
			fmt.Println("Instalado. O serviço roda como root e inicia junto com o Mac.")
		}
	case "desinstalar", "uninstall":
		err = cmdUninstall()
	case "daemon":
		err = daemon.Run()
	case "watchdog":
		err = daemon.RunWatchdog()
	case "-h", "--help", "help", "ajuda":
		fmt.Print(usage)
	default:
		err = fmt.Errorf("comando desconhecido %q\n\n%s", cmd, usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		os.Exit(1)
	}
}

func localTZ() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	// /etc/localtime -> /var/db/timezone/zoneinfo/America/Sao_Paulo
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			return target[i+len("zoneinfo/"):]
		}
	}
	return time.Local.String()
}

// parseDuration aceita o formato do Go (1h30m) e também dias: "2d", "1d12h".
func parseDuration(s string) (time.Duration, error) {
	var days time.Duration
	if before, after, ok := strings.Cut(s, "d"); ok {
		n, err := strconv.Atoi(before)
		if err != nil {
			return 0, fmt.Errorf("duração inválida %q", s)
		}
		days = time.Duration(n) * 24 * time.Hour
		s = after
		if s == "" {
			return days, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("duração inválida %q (ex.: 45m, 2h, 1d)", s)
	}
	return days + d, nil
}

func confirm(prompt string, yes bool) bool {
	if yes {
		return true
	}
	fmt.Print(prompt + " [s/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "s" || line == "sim" || line == "y" || line == "yes"
}

func cmdLock(args []string) error {
	fs := flag.NewFlagSet("bloquear", flag.ExitOnError)
	por := fs.String("por", "", "duração (ex.: 45m, 2h, 1d)")
	ate := fs.String("ate", "", "horário de término HH:MM")
	exceto := fs.String("exceto", "", "com \"internet\": sites que continuam acessíveis, separados por vírgula")
	yes := fs.Bool("s", false, "não pedir confirmação")
	_ = fs.Parse(args)
	req := api.Request{Cmd: api.CmdLock, Sites: fs.Args(), Allow: splitList(*exceto), Until: *ate, TZ: localTZ()}
	if *por != "" {
		d, err := parseDuration(*por)
		if err != nil {
			return err
		}
		req.For = d
	}
	if len(req.Sites) == 0 {
		return fmt.Errorf("informe os sites. Ex.: focolock bloquear --por 2h youtube.com")
	}
	if !confirm(fmt.Sprintf("Bloquear %s? Não dá pra cancelar depois.", describe(req.Sites, req.Allow)), *yes) {
		return fmt.Errorf("cancelado")
	}
	resp, err := api.Call(req, 10*time.Second)
	if err != nil {
		return err
	}
	fmt.Println(resp.Message)
	return nil
}

func cmdSchedule(args []string) error {
	fs := flag.NewFlagSet("agendar", flag.ExitOnError)
	dias := fs.String("dias", "seg-sex", "dias da semana")
	das := fs.String("das", "", "início HH:MM")
	ate := fs.String("ate", "", "fim HH:MM (pode ser menor que o início: atravessa a meia-noite)")
	nome := fs.String("nome", "", "nome do agendamento")
	exceto := fs.String("exceto", "", "com \"internet\": sites que continuam acessíveis, separados por vírgula")
	yes := fs.Bool("s", false, "não pedir confirmação")
	_ = fs.Parse(args)
	if *das == "" || *ate == "" {
		return fmt.Errorf("informe --das e --ate. Ex.: focolock agendar --dias seg-sex --das 09:00 --ate 18:00 youtube.com")
	}
	days, err := state.ParseDays(*dias)
	if err != nil {
		return err
	}
	if len(fs.Args()) == 0 {
		return fmt.Errorf("informe os sites")
	}
	prompt := fmt.Sprintf("Bloquear %s em %s das %s às %s? Remover depois leva %v.",
		describe(fs.Args(), splitList(*exceto)), state.FormatDays(days), *das, *ate, state.RemovalDelay)
	if !confirm(prompt, *yes) {
		return fmt.Errorf("cancelado")
	}
	resp, err := api.Call(api.Request{
		Cmd: api.CmdScheduleAdd, Sites: fs.Args(), Allow: splitList(*exceto), Name: *nome,
		Days: days, From: *das, To: *ate, TZ: localTZ(),
	}, 10*time.Second)
	if err != nil {
		return err
	}
	fmt.Println(resp.Message)
	return nil
}

func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

// describe mostra os sites como na app: "*" é "toda a internet".
func describe(list, allow []string) string {
	out := make([]string, len(list))
	for i, s := range list {
		if s == "*" || s == "internet" {
			s = "toda a internet"
			if len(allow) > 0 {
				s += " (exceto " + strings.Join(allow, ", ") + ")"
			}
		}
		out[i] = s
	}
	return strings.Join(out, ", ")
}

// parsePeriods lê "06:00-12:00=10,18:00-23:59=10".
func parsePeriods(s string) ([]api.Period, error) {
	var out []api.Period
	for _, part := range strings.Split(s, ",") {
		span, mins, ok := strings.Cut(strings.TrimSpace(part), "=")
		from, to, ok2 := strings.Cut(span, "-")
		n, err := strconv.Atoi(strings.TrimSuffix(mins, "m"))
		if !ok || !ok2 || err != nil {
			return nil, fmt.Errorf("período inválido %q (use HH:MM-HH:MM=minutos)", part)
		}
		out = append(out, api.Period{From: from, To: to, Minutes: n})
	}
	return out, nil
}

func cmdTimer(args []string) error {
	fs := flag.NewFlagSet("temporizador", flag.ExitOnError)
	periodos := fs.String("periodos", "", "períodos, ex.: 06:00-12:00=10,18:00-23:59=10")
	nome := fs.String("nome", "", "nome do temporizador")
	exceto := fs.String("exceto", "", "com \"internet\": sites que continuam acessíveis, separados por vírgula")
	yes := fs.Bool("s", false, "não pedir confirmação")
	_ = fs.Parse(args)
	if *periodos == "" {
		return fmt.Errorf("informe --periodos. Ex.: focolock temporizador --periodos 06:00-12:00=10 youtube.com")
	}
	periods, err := parsePeriods(*periodos)
	if err != nil {
		return err
	}
	if len(fs.Args()) == 0 {
		return fmt.Errorf("informe os sites")
	}
	prompt := fmt.Sprintf("Bloquear %s sempre, com pausas em %s? Remover depois leva %v.",
		describe(fs.Args(), splitList(*exceto)), *periodos, state.RemovalDelay)
	if !confirm(prompt, *yes) {
		return fmt.Errorf("cancelado")
	}
	resp, err := api.Call(api.Request{
		Cmd: api.CmdTimerAdd, Sites: fs.Args(), Allow: splitList(*exceto), Name: *nome, Periods: periods, TZ: localTZ(),
	}, 10*time.Second)
	if err != nil {
		return err
	}
	fmt.Println(resp.Message)
	return nil
}

func cmdTimerUse(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("uso: focolock usar <id do temporizador> <período: 1, 2, …>")
	}
	n, err := strconv.Atoi(args[1])
	if err != nil || n < 1 {
		return fmt.Errorf("período inválido %q (1 é o primeiro)", args[1])
	}
	resp, err := api.Call(api.Request{Cmd: api.CmdTimerUse, ID: args[0], Period: n - 1, TZ: localTZ()}, 10*time.Second)
	if err != nil {
		return err
	}
	fmt.Println(resp.Message)
	return nil
}

func cmdRemove(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("uso: focolock remover <id do agendamento ou temporizador>")
	}
	resp, err := api.Call(api.Request{Cmd: api.CmdScheduleRemove, ID: args[0], TZ: localTZ()}, 10*time.Second)
	if err != nil {
		return err
	}
	fmt.Println(resp.Message)
	return nil
}

func cmdStatus() error {
	resp, err := api.Call(api.Request{Cmd: api.CmdStatus}, 5*time.Second)
	if err != nil {
		return err
	}
	now := resp.Now.Local()
	fmt.Printf("Hora confiável: %s", now.Format("02/01/2006 15:04:05"))
	if drift := time.Until(now).Round(time.Second); drift > 5*time.Second || drift < -5*time.Second {
		fmt.Printf("  (o relógio do Mac está %v fora; mudar a hora não adianta 😉)", -drift)
	}
	fmt.Println()
	if len(resp.Active) == 0 {
		fmt.Println("\nNada bloqueado agora.")
	} else {
		fmt.Printf("\nBloqueado agora: %s\n", describe(resp.Active, resp.Allow))
	}
	st := resp.State
	if len(st.Locks) > 0 {
		fmt.Println("\nBloqueios:")
		for _, l := range st.Locks {
			left := l.End.Sub(resp.Now).Round(time.Minute)
			fmt.Printf("  %s  até %s (faltam %v)  %s\n", l.ID, l.End.Local().Format("02/01 15:04"), left, describe(l.Sites, l.Allow))
		}
	}
	if len(st.Schedules) > 0 {
		fmt.Println("\nAgendamentos:")
		for _, s := range st.Schedules {
			mark := " "
			if s.Active(resp.Now) {
				mark = "●"
			}
			name := s.Name
			if name == "" {
				name = "-"
			}
			fmt.Printf(" %s %s  %-12s %s %s–%s  %s", mark, s.ID, name, state.FormatDays(s.Days),
				state.FormatHHMM(s.From), state.FormatHHMM(s.To), describe(s.Sites, s.Allow))
			if s.RemoveAfter != nil {
				fmt.Printf("  (sai em %s)", s.RemoveAfter.Local().Format("02/01 15:04"))
			}
			fmt.Println()
		}
	}
	if len(st.Timers) > 0 {
		fmt.Println("\nTemporizadores:")
		labels := map[string]string{
			state.PeriodAvailable: "disponível", state.PeriodInUse: "a usar",
			state.PeriodUsed: "já usado hoje", state.PeriodOutside: "fora do horário",
		}
		for _, t := range st.Timers {
			name := t.Name
			if name == "" {
				name = "-"
			}
			fmt.Printf("   %s  %-12s %s", t.ID, name, describe(t.Sites, t.Allow))
			if t.RemoveAfter != nil {
				fmt.Printf("  (sai em %s)", t.RemoveAfter.Local().Format("02/01 15:04"))
			}
			fmt.Println()
			for i, p := range t.Periods {
				status, end := t.PeriodStatus(i, resp.Now)
				label := labels[status]
				if status == state.PeriodInUse {
					label += fmt.Sprintf(" (faltam %v)", end.Sub(resp.Now).Round(time.Second))
				}
				fmt.Printf("      %d. %s–%s  %d min  %s\n", i+1, state.FormatHHMM(p.From), state.FormatHHMM(p.To), p.Minutes, label)
			}
		}
	}
	return nil
}

func cmdUninstall() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("precisa de root: sudo focolock desinstalar")
	}
	guarded := false
	if resp, err := api.Call(api.Request{Cmd: api.CmdStatus}, 3*time.Second); err == nil {
		guarded = resp.Guarded
	} else {
		guarded = guard.Store.Load().Guarded(time.Now())
	}
	if guarded {
		return fmt.Errorf("há bloqueios, agendamentos ou temporizadores ativos. Espere terminarem (agendamentos e temporizadores: focolock remover <id>, efetivo em %v)", state.RemovalDelay)
	}
	if err := guard.Uninstall(); err != nil {
		return err
	}
	// Remove a seção do /etc/hosts e esvazia o anchor do firewall.
	(&enforce.Enforcer{ResolverDir: enforce.DefaultResolverDir}).Apply(nil, nil)
	fmt.Println("Desinstalado.")
	return nil
}
