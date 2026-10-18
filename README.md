# focolock

Bloqueador de sites para macOS no estilo **Cold Turkey**: você define um
horário (ou uma duração) e os sites ficam bloqueados — mesmo que você feche o
terminal, mate o processo, reinicie o Mac ou mude a hora do relógio.

**App:** depois de instalar, abra o **Focolock** pelo Launchpad ou Spotlight.
Cola o link, escolhe a duração e carrega em Bloquear. Fechar a janela não
desbloqueia nada: ela é só uma interface para o serviço que corre em segundo
plano (servida em `http://127.0.0.1:47811`, só acessível a partir do próprio Mac).

Também pelo Terminal:

```sh
focolock bloquear --por 2h youtube.com x.com
focolock bloquear --ate 18:00 instagram.com
focolock bloquear --por 2h --exceto wikipedia.org,github.com internet   # toda a internet, menos estes
focolock agendar --dias seg-sex --das 09:00 --ate 18:00 --nome trabalho youtube.com reddit.com
focolock agendar --dias todos --das 23:00 --ate 07:00 --nome sono x.com    # atravessa a meia-noite
focolock temporizador --periodos 06:00-12:00=10,18:00-23:59=10 --nome redes instagram.com
focolock usar <id> 1           # gasta os 10 min do 1.º período (uma vez por dia)
focolock status
focolock remover <id>          # só vale daqui a 24h, e nunca no meio de uma janela ativa
```

**Toda a internet, com exceções:** na app escolha "Toda a internet" em vez de
"Sites" e adicione os sites que devem continuar a funcionar (no Terminal,
`internet` com `--exceto`). Vale para bloqueios, horários fixos e
temporizadores. Só passa o tráfego para os IPs desses sites (re-resolvidos a
cada 2 min) e o DNS comum; o resto fica cortado. Um site que carrega conteúdo
de outros domínios (ex.: YouTube usa `googlevideo.com` e `ytimg.com`) precisa
deles também na lista. Se dois bloqueios de internet estiverem ativos ao
mesmo tempo, só passa o que os dois permitem.

**Temporizador:** os sites ficam sempre bloqueados. Em cada período do dia
pode desbloqueá-los uma única vez, durante os minutos definidos; depois voltam
a bloquear sozinhos. Não dá para pausar nem repetir, e conta a hora confiável
do serviço, por isso mudar a hora do Mac não dá minutos extra. A pausa também
abre esses sites por cima de um bloqueio de toda a internet (ex.: horário que
bloqueia a internet das 09:00 às 18:00 + temporizador do YouTube com 10 min);
um site bloqueado um a um por outra regra continua bloqueado.

Não existe comando para cancelar ou encurtar um bloqueio. Esse é o ponto.

## Instalação

Sem compilar nada (Intel e Apple Silicon):

```sh
cd ~ && curl -L https://raw.githubusercontent.com/pedroivy/focolock/claude/affectionate-volta-g0crb5/bin/focolock -o focolock && chmod +x focolock && sudo ./focolock instalar
```

Ou a partir do código, com Go 1.22+ (`brew install go`):

```sh
git clone <este repo> && cd focolock
make build
sudo ./focolock instalar
```

Isso instala dois LaunchDaemons que rodam como **root** e sobem no boot,
antes de qualquer login:

| Serviço | Função |
|---|---|
| `com.focolock.daemon` | aplica os bloqueios a cada 2s e atende a CLI pelo socket `/var/run/focolock.sock` |
| `com.focolock.watchdog` | vigia o daemon e o restaura se for desligado |

Para atualizar: `git pull && make build && sudo ./focolock instalar`.
Para remover: `sudo focolock desinstalar` (recusa enquanto houver bloqueio ou agendamento).

## Como o bloqueio funciona

1. **`/etc/hosts`** — os sites (e `www.`, `m.`, `mobile.`, `app.`, `api.`, `web.`)
   apontam para `0.0.0.0`. Se você editar o arquivo, o daemon reescreve em até 2s.
2. **Firewall (pf)** — os IPs dos sites são resolvidos por DNS externo e
   bloqueados num anchor `com.apple/250.focolock`, que o `pf.conf` padrão já
   carrega. Conexões já abertas são derrubadas. Re-resolvido a cada 10 min.
3. **DNS seguro desligado à força** — durante um bloqueio, os servidores de
   DNS-over-HTTPS (Google, Cloudflare, Quad9, NextDNS…) e o iCloud Private
   Relay são bloqueados, senão Chrome/Firefox/Safari ignorariam o `/etc/hosts`.
   O DNS comum (porta 53) continua funcionando.

## Proteções contra burla

| Tentativa | O que acontece |
|---|---|
| Fechar o terminal/app | Nada: quem bloqueia é o daemon root, não a CLI |
| `kill -9` no daemon | O `launchd` (KeepAlive) reabre na hora; e o hosts/pf **não** são limpos ao sair |
| `launchctl bootout` / `disable` | O watchdog dá `enable` + `bootstrap` de volta em ≤5s (e vice-versa) |
| Apagar plist ou binário | Restaurados a partir de uma cópia em `/var/db/focolock` |
| Editar/apagar o arquivo de estado | O estado vive na memória do daemon, é salvo em 2 lugares e unido na leitura; o watchdog guarda outra cópia |
| Editar `/etc/hosts` / `pfctl -d` / `pfctl -F` | Reaplicado em ≤2s |
| Adiantar o relógio | Relógio monotônico (que conta o tempo dormindo) detecta o pulo e compensa; NTP recalibra quando online |
| Mudar o fuso horário | O fuso fica gravado no agendamento |
| Reiniciar o Mac | Os daemons sobem no boot; o hosts já está bloqueado antes disso |
| `sudo focolock desinstalar` | Recusado enquanto houver bloqueio |

**Honestidade:** você é administrador da própria máquina. Alguém determinado
consegue sair (Modo de Recuperação, apagar tudo de uma vez num script como
root, outro usuário/VPN com DNS próprio…). O objetivo — o mesmo do Cold
Turkey — é tornar a fuga trabalhosa o bastante para não valer a pena no
impulso.

## Observações

- Bloquear por IP é forte, mas sites atrás de CDN compartilhada podem levar
  junto algum outro site do mesmo IP.
- Alguns domínios nunca podem ser bloqueados (`apple.com`, `icloud.com`,
  `localhost`, `pool.ntp.org`) para não quebrar o sistema nem o relógio.
- Bloqueio máximo de 90 dias (proteção contra erro de digitação).
- Logs: `/var/log/focolock.log`.

## Desenvolvimento

```sh
make test       # roda em Linux também; a parte de pf/launchd é só darwin
```

Estrutura:

```
cmd/focolock        CLI + subcomandos daemon/watchdog
internal/state      bloqueios, agendamentos, regras de remoção, persistência
internal/clock      relógio à prova de mudança de hora (monotônico + NTP)
internal/enforce    /etc/hosts, pf, flush de DNS
internal/guard      LaunchDaemons, autoproteção, instalar/desinstalar
internal/daemon     loop principal, socket da API, watchdog
internal/api        protocolo CLI <-> daemon
```

## Próximos passos

- Bloqueio de apps (matar processos por nome durante o bloqueio)
- Extensão de navegador para bloquear por caminho (ex.: só `youtube.com/shorts`)
