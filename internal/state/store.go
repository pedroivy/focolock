package state

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Store grava o estado em dois lugares; na leitura os dois são unidos.
type Store struct {
	Primary, Backup string
}

func (s Store) Load() *State {
	a, errA := readFile(s.Primary)
	b, errB := readFile(s.Backup)
	// Arquivo ausente ou corrompido conta como vazio; o outro ainda vale.
	if errA != nil {
		a = &State{}
	}
	if errB != nil {
		b = &State{}
	}
	return Merge(a, b)
}

func (s Store) Save(st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	var firstErr error
	for _, p := range []string{s.Primary, s.Backup} {
		if err := WriteAtomic(p, data, 0o644); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func readFile(p string) (*State, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// WriteAtomic escreve num arquivo temporário e renomeia por cima.
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path))
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
