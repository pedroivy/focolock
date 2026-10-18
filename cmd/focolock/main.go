// focolock: bloqueador de sites para macOS no estilo Cold Turkey.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
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
  focolock agendar --dias seg-sex --das 09:00 --ate 18:00 [--nome trabalho] youtube.com
  focolock status                                   mostra o que está bloqueado
  focolock remover <id>                             remove um agendamento (após 24h)

  sudo focolock instalar                            instala/atualiza o serviço
  sudo focolock desinstalar                         só funciona sem bloqueios ativos

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
	case "status", "st":
		err = cmdStatus()
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
	yes := fs.Bool("s", false, "não pedir confirmação")
	_ = fs.Parse(args)
	req := api.Request{Cmd: api.CmdLock, Sites: fs.Args(), Until: *ate, TZ: localTZ()}
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
	if !confirm(fmt.Sprintf("Bloquear %s? Não dá pra cancelar depois.", strings.Join(req.Sites, ", ")), *yes) {
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
		strings.Join(fs.Args(), ", "), state.FormatDays(days), *das, *ate, state.RemovalDelay)
	if !confirm(prompt, *yes) {
		return fmt.Errorf("cancelado")
	}
	resp, err := api.Call(api.Request{
		Cmd: api.CmdScheduleAdd, Sites: fs.Args(), Name: *nome,
		Days: days, From: *das, To: *ate, TZ: localTZ(),
	}, 10*time.Second)
	if err != nil {
		return err
	}
	fmt.Println(resp.Message)
	return nil
}

func cmdRemove(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("uso: focolock remover <id do agendamento>")
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
		fmt.Printf("\nBloqueado agora: %s\n", strings.Join(resp.Active, ", "))
	}
	st := resp.State
	if len(st.Locks) > 0 {
		fmt.Println("\nBloqueios:")
		for _, l := range st.Locks {
			left := l.End.Sub(resp.Now).Round(time.Minute)
			fmt.Printf("  %s  até %s (faltam %v)  %s\n", l.ID, l.End.Local().Format("02/01 15:04"), left, strings.Join(l.Sites, ", "))
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
				state.FormatHHMM(s.From), state.FormatHHMM(s.To), strings.Join(s.Sites, ", "))
			if s.RemoveAfter != nil {
				fmt.Printf("  (sai em %s)", s.RemoveAfter.Local().Format("02/01 15:04"))
			}
			fmt.Println()
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
		return fmt.Errorf("há bloqueios ou agendamentos ativos. Espere terminarem (agendamentos: focolock remover <id>, efetivo em %v)", state.RemovalDelay)
	}
	if err := guard.Uninstall(); err != nil {
		return err
	}
	// Remove a seção do /etc/hosts e esvazia o anchor do firewall.
	(&enforce.Enforcer{}).Apply(nil)
	fmt.Println("Desinstalado.")
	return nil
}
