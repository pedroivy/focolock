// Package guard instala o focolock como LaunchDaemons e o protege: dois
// processos root (daemon e watchdog) vigiam um ao outro e restauram binário,
// plists e serviços se alguém mexer enquanto houver bloqueio.
package guard

import "github.com/pedroivy/focolock/internal/state"

const (
	BinPath    = "/usr/local/libexec/focolock"
	LinkPath   = "/usr/local/bin/focolock"
	DataDir    = "/Library/Application Support/focolock"
	BackupDir  = "/var/db/focolock"
	BackupBin  = BackupDir + "/focolock.bin"
	LogPath    = "/var/log/focolock.log"
	DaemonName = "com.focolock.daemon"
	WatchName  = "com.focolock.watchdog"
)

func PlistPath(label string) string { return "/Library/LaunchDaemons/" + label + ".plist" }

var Store = state.Store{
	Primary: DataDir + "/state.json",
	Backup:  BackupDir + "/state.json",
}

// Plist gera o LaunchDaemon. KeepAlive faz o launchd reabrir o processo se ele
// for morto; RunAtLoad o inicia no boot, antes de qualquer login.
func Plist(label, mode string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + BinPath + `</string>
		<string>` + mode + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>2</integer>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>StandardOutPath</key>
	<string>` + LogPath + `</string>
	<key>StandardErrorPath</key>
	<string>` + LogPath + `</string>
</dict>
</plist>
`
}

// Services: rótulo -> subcomando.
var Services = map[string]string{
	DaemonName: "daemon",
	WatchName:  "watchdog",
}
