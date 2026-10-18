//go:build !darwin

package guard

import "fmt"

func Ensure() {}

func Install() error   { return fmt.Errorf("o focolock só pode ser instalado no macOS") }
func Uninstall() error { return fmt.Errorf("o focolock só pode ser instalado no macOS") }
