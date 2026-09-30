//go:build windows
// +build windows

package store

import (
	"fmt"
	"syscall"
	"unsafe"
)

// DPAPI (Data Protection API) — штатное шифрование Windows, доступное
// с Windows 2000, то есть и на XP. Ключ выводится из учётных данных
// текущего пользователя и хранится системой: пароль спрашивать не
// нужно, а зашифрованный блоб, скопированный на другой компьютер или
// открытый под другой учётной записью, не расшифровать.
//
// Вызывается напрямую через syscall — новых зависимостей (и
// golang.org/x/sys, которого нет в запиненном GOPATH) не требуется.
var (
	crypt32       = syscall.NewLazyDLL("crypt32.dll")
	kernel32      = syscall.NewLazyDLL("kernel32.dll")
	procProtect   = crypt32.NewProc("CryptProtectData")
	procUnprotect = crypt32.NewProc("CryptUnprotectData")
	procLocalFree = kernel32.NewProc("LocalFree")
)

// cryptProtectUIForbidden — никогда не показывать диалогов: у нас
// GUI-приложение, но неожиданное окно от системного API нам не нужно.
const cryptProtectUIForbidden = 0x1

// dataBlob — DATA_BLOB из wincrypt.h.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(data []byte) *dataBlob {
	if len(data) == 0 {
		return &dataBlob{}
	}
	return &dataBlob{cbData: uint32(len(data)), pbData: &data[0]}
}

// takeBlob копирует содержимое выходного блоба в обычный слайс и
// освобождает память, выделенную системой (LocalFree).
func takeBlob(out *dataBlob) []byte {
	if out.pbData == nil {
		return nil
	}
	n := int(out.cbData)
	res := make([]byte, n)
	copy(res, (*[1 << 30]byte)(unsafe.Pointer(out.pbData))[:n:n])
	procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return res
}

// protect шифрует data через CryptProtectData. entropy — необязательная
// "соль" уровня приложения: расшифровать блоб сможет только тот, кто
// передаст те же байты, так что другие программы того же пользователя
// не расшифруют его "случайно" тем же вызовом без знания константы.
func protect(data, entropy []byte) ([]byte, error) {
	in := newBlob(data)
	ent := newBlob(entropy)
	var out dataBlob

	r, _, err := procProtect.Call(
		uintptr(unsafe.Pointer(in)),
		0, // szDataDescr
		uintptr(unsafe.Pointer(ent)),
		0, // pvReserved
		0, // pPromptStruct
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptProtectData: %v", err)
	}
	return takeBlob(&out), nil
}

// unprotect — обратная операция, CryptUnprotectData.
func unprotect(blob, entropy []byte) ([]byte, error) {
	in := newBlob(blob)
	ent := newBlob(entropy)
	var out dataBlob

	r, _, err := procUnprotect.Call(
		uintptr(unsafe.Pointer(in)),
		0, // ppszDataDescr
		uintptr(unsafe.Pointer(ent)),
		0, // pvReserved
		0, // pPromptStruct
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptUnprotectData: %v", err)
	}
	return takeBlob(&out), nil
}
