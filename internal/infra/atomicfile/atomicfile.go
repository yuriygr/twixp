// Package atomicfile — запись файла целиком "или старая версия, или
// новая, но никогда не половина".
package atomicfile

import (
	"io/ioutil"
	"os"
	"path/filepath"
)

// Write пишет data во временный файл рядом с path и переименовывает
// его поверх целевого: обрыв питания или падение посередине записи не
// оставит наполовину записанный (а значит нечитаемый) файл.
//
// Имя временного файла уникально (ioutil.TempFile), поэтому две
// параллельные записи в один и тот же path не наступают друг другу на
// ноги на общем "path.tmp". Права у файла — 0600 (так создаёт
// TempFile). Каталог должен уже существовать.
//
// Замечание для Windows: Rename поверх файла, который в этот момент
// открыт другим читателем, может не удаться — вызывающий код, для
// которого запись необязательна (кэш), просто игнорирует такую
// ошибку.
func Write(path string, data []byte) error {
	f, err := ioutil.TempFile(filepath.Dir(path), filepath.Base(path)+".")
	if err != nil {
		return err
	}
	tmp := f.Name()

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
