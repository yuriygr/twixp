// Package imgcache — дисковый кэш картинок (аватарки каналов, иконки
// бейджей) поверх любого способа их скачать.
//
// Ключ кэша — сам URL. Twitch отдаёт такие картинки по неизменяемым
// адресам (новая аватарка получает новое имя файла, у бейджа свой
// UUID), поэтому устаревшего содержимого под тем же URL не бывает, и
// проверять свежесть или перекачивать не нужно: устаревший файл
// просто перестаёт запрашиваться. Ограничивать приходится только
// размер кэша на диске — самые давно не использованные файлы
// вытесняются (см. Cache.prune).
package imgcache

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg" // регистрация декодеров для image.Decode ниже
	_ "image/png"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"twixp/internal/infra/atomicfile"
)

const (
	fileExt = ".img"
	// staleTempAge — через сколько подвисший временный файл (остаток
	// от падения посреди записи) считается мусором и удаляется.
	staleTempAge = time.Hour
)

// Downloader скачивает сырые байты картинки по URL.
type Downloader func(url string) ([]byte, error)

// Cache — дисковый кэш. Безопасен для параллельного использования.
type Cache struct {
	dir      string
	maxBytes int64
	download Downloader

	pruneMu sync.Mutex
}

// New создаёт кэш в каталоге dir (создаётся при первой записи) с
// ограничением maxBytes на суммарный размер файлов. download зовётся
// только при промахе.
func New(dir string, maxBytes int64, download Downloader) *Cache {
	return &Cache{dir: dir, maxBytes: maxBytes, download: download}
}

func (c *Cache) path(url string) string {
	sum := sha1.Sum([]byte(url))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:])+fileExt)
}

// Fetch возвращает декодированную картинку: с диска, если она там
// есть, иначе скачивает, сохраняет и возвращает. Подходит под
// ui.ImageFetcher (метод-значение c.Fetch).
//
// Сбой кэша (нельзя прочитать, записать, повреждён файл) никогда не
// превращается в ошибку для вызывающего кода — кэш лишь оптимизация,
// в худшем случае просто идём в сеть, как и без него. Ошибкой
// возвращается только то, что не удалось получить саму картинку.
func (c *Cache) Fetch(url string) (image.Image, error) {
	p := c.path(url)

	if data, err := ioutil.ReadFile(p); err == nil {
		if img, _, derr := image.Decode(bytes.NewReader(data)); derr == nil {
			// Отметка "использовалось сейчас" — по ней вытесняем самое
			// давно не нужное.
			now := time.Now()
			os.Chtimes(p, now, now)
			return img, nil
		}
		log.Printf("imgcache: повреждённый файл кэша, перекачиваем: %s", filepath.Base(p))
		os.Remove(p)
	}

	data, err := c.download(url)
	if err != nil {
		return nil, err
	}

	// Декодируем ДО записи: в кэш попадает только то, что реально
	// картинка (а не, скажем, HTML-страница с ошибкой от прокси).
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("декодировать: %v", err)
	}

	if err := os.MkdirAll(c.dir, 0700); err != nil {
		log.Println("imgcache: создать каталог:", err)
		return img, nil
	}
	if err := atomicfile.Write(p, data); err != nil {
		log.Println("imgcache: записать:", err)
		return img, nil
	}

	c.prune()
	return img, nil
}

// prune держит суммарный размер кэша в пределах maxBytes: удаляет
// самые давно использованные файлы (по времени изменения, которое
// Fetch обновляет при каждом чтении). Заодно убирает давно
// оставшиеся временные файлы от прерванных записей.
func (c *Cache) prune() {
	c.pruneMu.Lock()
	defer c.pruneMu.Unlock()

	entries, err := ioutil.ReadDir(c.dir)
	if err != nil {
		return
	}

	var files []os.FileInfo
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), fileExt) {
			if time.Since(e.ModTime()) > staleTempAge {
				os.Remove(filepath.Join(c.dir, e.Name()))
			}
			continue
		}
		files = append(files, e)
		total += e.Size()
	}

	if total <= c.maxBytes {
		return
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime().Before(files[j].ModTime())
	})
	for _, f := range files {
		if total <= c.maxBytes {
			break
		}
		if err := os.Remove(filepath.Join(c.dir, f.Name())); err == nil {
			total -= f.Size()
		}
	}
}
