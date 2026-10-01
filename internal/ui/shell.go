//go:build windows
// +build windows

package ui

import (
	"fmt"
	"net/url"
	"syscall"
	"unsafe"
)

// ShellExecuteW из shell32 — у lxn/walk и lxn/win на пиненных коммитах
// обёртки для него нет, а тащить ради одного вызова что-то ещё не нужно.
var procShellExecute = syscall.NewLazyDLL("shell32.dll").NewProc("ShellExecuteW")

const swShowNormal = 1

// openURL открывает адрес в браузере по умолчанию. Только http и https:
// ShellExecute умеет открывать что угодно (файлы, программы), и передавать
// ему непроверенную строку нельзя.
func openURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("открывать можно только http и https, получили %q", u.Scheme)
	}

	verb, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := syscall.UTF16PtrFromString(rawURL)
	if err != nil {
		return err
	}

	// По документации ShellExecute успех — значение больше 32, остальное
	// коды ошибок.
	r, _, callErr := procShellExecute.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0,
		0,
		swShowNormal,
	)
	if r <= 32 {
		return fmt.Errorf("ShellExecute: код %d (%v)", r, callErr)
	}
	return nil
}
