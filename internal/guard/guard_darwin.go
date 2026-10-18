package guard

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/pedroivy/focolock/internal/state"
)

func launchctl(args ...string) (string, error) {
	out, err := exec.Command("/bin/launchctl", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("launchctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func loaded(label string) bool {
	_, err := launchctl("print", "system/"+label)
	return err == nil
}

func fileHash(p string) []byte {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil
	}
	return h.Sum(nil)
}

func copyFile(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return state.WriteAtomic(dst, data, perm)
}

// Ensure restaura o que estiver faltando ou adulterado. Chamado periodicamente
// pelo daemon e pelo watchdog enquanto houver bloqueio.
func Ensure() {
	// 1. Binário: o backup é a referência.
	if b := fileHash(BackupBin); b != nil {
		if !bytes.Equal(fileHash(BinPath), b) {
			log.Printf("guard: restaurando binário em %s", BinPath)
			if err := copyFile(BackupBin, BinPath, 0o755); err != nil {
				log.Printf("guard: %v", err)
			}
		}
	} else if fileHash(BinPath) != nil {
		_ = copyFile(BinPath, BackupBin, 0o700)
	}
	// 2. Plists e serviços.
	for label, mode := range Services {
		p := PlistPath(label)
		want := []byte(Plist(label, mode))
		if cur, _ := os.ReadFile(p); !bytes.Equal(cur, want) {
			log.Printf("guard: restaurando %s", p)
			if err := state.WriteAtomic(p, want, 0o644); err != nil {
				log.Printf("guard: %v", err)
			}
		}
		// "launchctl disable" impediria o bootstrap; desfazemos sempre.
		_, _ = launchctl("enable", "system/"+label)
		if !loaded(label) {
			log.Printf("guard: recarregando %s", label)
			if _, err := launchctl("bootstrap", "system", p); err != nil {
				log.Printf("guard: %v", err)
			}
		}
	}
}

// Install copia o binário atual para os lugares definitivos e sobe os serviços.
func Install() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("precisa de root: sudo focolock instalar")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	for _, d := range []string{DataDir, BackupDir, "/usr/local/libexec", "/usr/local/bin"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	_ = os.Chmod(BackupDir, 0o700)
	// Atualização: para os serviços antes de trocar o binário.
	for label := range Services {
		if loaded(label) {
			_, _ = launchctl("bootout", "system/"+label)
		}
	}
	// Backup primeiro: o guard de um serviço antigo ainda vivo usa o backup
	// como referência e "restauraria" o binário velho.
	if err := copyFile(self, BackupBin, 0o700); err != nil {
		return err
	}
	if err := copyFile(self, BinPath, 0o755); err != nil {
		return err
	}
	_ = os.Remove(LinkPath)
	if err := os.Symlink(BinPath, LinkPath); err != nil {
		return err
	}
	Ensure()
	// Se um serviço sobreviveu ao bootout (o outro o religou), reinicia com o binário novo.
	for label := range Services {
		_, _ = launchctl("kickstart", "-k", "system/"+label)
	}
	return nil
}

// Uninstall remove tudo. Quem chama deve garantir antes que não há bloqueio.
func Uninstall() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("precisa de root: sudo focolock desinstalar")
	}
	for label := range Services {
		_, _ = launchctl("bootout", "system/"+label)
		_ = os.Remove(PlistPath(label))
	}
	for _, p := range []string{LinkPath, BinPath} {
		_ = os.Remove(p)
	}
	_ = os.RemoveAll(BackupDir)
	_ = os.RemoveAll(DataDir)
	return nil
}
