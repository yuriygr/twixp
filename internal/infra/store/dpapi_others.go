//go:build !windows
// +build !windows

package store

import "errors"

// Заглушка для не-Windows платформ: приложение целиком работает только
// под Windows, а этот файл существует лишь затем, чтобы ядро
// (domain/app/infra) собиралось и проверялось современным Go на
// любой хост-системе (make vet-core). Шифровать нечем — честно
// возвращаем ошибку, а не притворяемся, что защитили токен.
var errNoDPAPI = errors.New("DPAPI недоступен на этой платформе")

func protect(data, entropy []byte) ([]byte, error)   { return nil, errNoDPAPI }
func unprotect(blob, entropy []byte) ([]byte, error) { return nil, errNoDPAPI }
