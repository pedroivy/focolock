package guard

import (
	_ "embed"
	"os"
	"path/filepath"

	"github.com/pedroivy/focolock/internal/state"
)

const AppPath = "/Applications/Focolock.app"

//go:embed assets/Focolock.icns
var appIcon []byte

// O app é só um atalho que abre a janela servida pelo daemon. Fechá-lo não
// muda nada: quem bloqueia é o serviço.
const appScript = `#!/bin/sh
URL="http://127.0.0.1:47811/"
# Chrome/Edge/Brave em modo "app" abrem uma janela própria, sem abas.
for b in "Google Chrome" "Microsoft Edge" "Brave Browser"; do
  if [ -d "/Applications/$b.app" ]; then
    exec open -na "$b" --args --app="$URL" --window-size=680,860
  fi
done
exec open "$URL"
`

const appPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>Focolock</string>
	<key>CFBundleDisplayName</key>
	<string>Focolock</string>
	<key>CFBundleIdentifier</key>
	<string>com.focolock.app</string>
	<key>CFBundleExecutable</key>
	<string>Focolock</string>
	<key>CFBundleIconFile</key>
	<string>Focolock</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>1.0</string>
	<key>LSMinimumSystemVersion</key>
	<string>11.0</string>
</dict>
</plist>
`

func installApp() error {
	c := filepath.Join(AppPath, "Contents")
	files := []struct {
		path string
		data []byte
		perm os.FileMode
	}{
		{filepath.Join(c, "Info.plist"), []byte(appPlist), 0o644},
		{filepath.Join(c, "MacOS", "Focolock"), []byte(appScript), 0o755},
		{filepath.Join(c, "Resources", "Focolock.icns"), appIcon, 0o644},
	}
	for _, f := range files {
		if err := state.WriteAtomic(f.path, f.data, f.perm); err != nil {
			return err
		}
	}
	// Faz o Finder/Dock notarem o ícone novo.
	now := timeNow()
	_ = os.Chtimes(AppPath, now, now)
	return nil
}
