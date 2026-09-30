package imgcache

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := ioutil.TempDir("", "imgcache")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "cache") // не существует — Cache создаёт сам
}

func TestFetchMissThenHit(t *testing.T) {
	dir := tempDir(t)
	defer os.RemoveAll(filepath.Dir(dir))

	calls := 0
	data := pngBytes(t)
	c := New(dir, 1<<20, func(url string) ([]byte, error) {
		calls++
		return data, nil
	})

	for i := 0; i < 3; i++ {
		img, err := c.Fetch("https://cdn/a.png")
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != 4 {
			t.Fatalf("размер %v", img.Bounds())
		}
	}
	if calls != 1 {
		t.Fatalf("сеть вызвана %d раз, ожидали 1", calls)
	}

	// Другой URL — отдельная запись.
	if _, err := c.Fetch("https://cdn/b.png"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("после второго URL: %d вызовов, ожидали 2", calls)
	}
}

func TestFetchSurvivesRestart(t *testing.T) {
	dir := tempDir(t)
	defer os.RemoveAll(filepath.Dir(dir))
	data := pngBytes(t)

	c1 := New(dir, 1<<20, func(string) ([]byte, error) { return data, nil })
	if _, err := c1.Fetch("u"); err != nil {
		t.Fatal(err)
	}

	// "Новый запуск": сеть недоступна, но картинка есть на диске.
	c2 := New(dir, 1<<20, func(string) ([]byte, error) { return nil, errors.New("нет сети") })
	if _, err := c2.Fetch("u"); err != nil {
		t.Fatalf("должно отдаться из кэша без сети: %v", err)
	}
}

func TestCorruptFileIsRedownloaded(t *testing.T) {
	dir := tempDir(t)
	defer os.RemoveAll(filepath.Dir(dir))
	data := pngBytes(t)

	calls := 0
	c := New(dir, 1<<20, func(string) ([]byte, error) { calls++; return data, nil })
	if _, err := c.Fetch("u"); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(c.path("u"), []byte("мусор"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Fetch("u"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("повреждённый файл должен перекачаться: %d вызовов", calls)
	}
}

func TestGarbageIsNotCached(t *testing.T) {
	dir := tempDir(t)
	defer os.RemoveAll(filepath.Dir(dir))

	c := New(dir, 1<<20, func(string) ([]byte, error) { return []byte("<html>error</html>"), nil })
	if _, err := c.Fetch("u"); err == nil {
		t.Fatal("ожидали ошибку декодирования")
	}
	if _, err := os.Stat(c.path("u")); err == nil {
		t.Fatal("мусор не должен попадать в кэш")
	}
}

func TestPruneEvictsLeastRecentlyUsed(t *testing.T) {
	dir := tempDir(t)
	defer os.RemoveAll(filepath.Dir(dir))
	data := pngBytes(t)
	size := int64(len(data))

	// Лимит на два файла.
	c := New(dir, size*2+size/2, func(string) ([]byte, error) { return data, nil })

	for _, u := range []string{"a", "b"} {
		if _, err := c.Fetch(u); err != nil {
			t.Fatal(err)
		}
	}
	// Состарим "a" и "b" явно, "a" — сильнее.
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(c.path("a"), old, old)
	mid := time.Now().Add(-1 * time.Hour)
	os.Chtimes(c.path("b"), mid, mid)

	// Чтение "a" освежает её — теперь самая старая "b".
	if _, err := c.Fetch("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Fetch("c"); err != nil { // третий файл — превышение лимита
		t.Fatal(err)
	}

	if _, err := os.Stat(c.path("b")); err == nil {
		t.Fatal("b должна быть вытеснена как самая давно не использованная")
	}
	for _, u := range []string{"a", "c"} {
		if _, err := os.Stat(c.path(u)); err != nil {
			t.Fatalf("%s должна остаться: %v", u, err)
		}
	}
}

func TestStaleTempFilesRemoved(t *testing.T) {
	dir := tempDir(t)
	defer os.RemoveAll(filepath.Dir(dir))
	data := pngBytes(t)

	os.MkdirAll(dir, 0700)
	stale := filepath.Join(dir, "x.img.123")
	fresh := filepath.Join(dir, "y.img.456")
	ioutil.WriteFile(stale, []byte("x"), 0600)
	ioutil.WriteFile(fresh, []byte("y"), 0600)
	old := time.Now().Add(-3 * time.Hour)
	os.Chtimes(stale, old, old)

	c := New(dir, 1<<20, func(string) ([]byte, error) { return data, nil })
	if _, err := c.Fetch("u"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(stale); err == nil {
		t.Fatal("старый временный файл должен быть удалён")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("свежий временный файл (возможно, идёт запись) трогать нельзя")
	}
}
