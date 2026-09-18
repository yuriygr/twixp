// Package nettls — единственное место, где решается, каким корневым
// сертификатам доверять и какую версию TLS предлагать для ВСЕХ
// исходящих соединений приложения (Helix REST, Device Code Flow,
// EventSub WebSocket). До этого пакета каждый из трёх мест сам себе
// заводил tls.Config/http.Client — ровно это и привело к тому, что
// правка для одного клиента (helix) не подхватилась другим
// (device_flow), пока не потратили час на выяснение почему.
//
// Зачем вообще нужен свой RootCAs, а не пустой tls.Config{}: до Go
// 1.18 crypto/tls на Windows не умел читать системное хранилище
// сертификатов (x509.SystemCertPool() либо возвращал пустой пул, либо
// ошибку — нативная поддержка появилась через 4 года после нашего
// целевого Go 1.10.8). На чистой/старой Windows XP это означает, что
// без явно заданного RootCAs у TLS-клиента нет вообще ни одного
// доверенного корня — любое TLS-соединение обречено на
// "certificate signed by unknown authority", независимо от сети и
// от того, кто выпустил сертификат сервера. Та же болезнь у других
// клиентов id.twitch.tv на похожих окружениях:
// https://github.com/TwitchIO/TwitchIO/issues/270
//
// Поэтому корневые сертификаты зашиты прямо в бинарник (rootcerts.go)
// — не полагаемся на ОС вовсе, работает одинаково что на XP, что на
// маке, что на актуальной Windows.
package nettls

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"sync"
	"time"
)

var (
	rootCAsOnce sync.Once
	rootCAsPool *x509.CertPool
)

// RootCAs — общий пул доверенных корневых сертификатов для всех
// исходящих TLS-соединений приложения. Строится один раз.
func RootCAs() *x509.CertPool {
	rootCAsOnce.Do(func() {
		rootCAsPool = x509.NewCertPool()
		if !rootCAsPool.AppendCertsFromPEM([]byte(mozillaCABundlePEM)) {
			// Не должно случиться с валидным бандлом — но если вдруг,
			// лучше явно упасть при старте, чем молча остаться с
			// пустым пулом и получить неотличимые от уже пройденного
			// бага симптомы ("сеть не работает"), пока кто-то не
			// потратит ещё один час на выяснение.
			panic("nettls: не удалось разобрать встроенный набор корневых сертификатов")
		}
	})
	return rootCAsPool
}

// HTTPTransport — общий *http.Transport для клиентов на базе
// net/http (Helix, Device Code Flow). ServerName транспорт выставляет
// сам по адресу каждого запроса — здесь его задавать не нужно (и
// нельзя: один транспорт используется для разных хостов).
func HTTPTransport() *http.Transport {
	return &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    RootCAs(),
			MinVersion: tls.VersionTLS12,
		},
	}
}

// DefaultTimeout — таймаут по умолчанию для всех HTTP-клиентов
// приложения (Helix, Device Code Flow, загрузка аватарок). Раньше
// каждый потребитель писал одно и то же "15 * time.Second" на месте —
// не страшно само по себе, но при следующей правке таймаута легко
// поправить три места из четырёх и не заметить.
const DefaultTimeout = 15 * time.Second

// NewHTTPClient — общий *http.Client (та же TLS-конфигурация, что и
// HTTPTransport) с заданным таймаутом. Без этой обёртки каждый новый
// потребитель (Helix, Device Code Flow, а теперь ещё и загрузка
// аватарок) заново писал бы один и тот же
// &http.Client{Transport: HTTPTransport(), Timeout: ...} — то самое
// дублирование, из-за которого чинить TLS пришлось в трёх местах
// вместо одного.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: HTTPTransport(),
		Timeout:   timeout,
	}
}

// DialTLSConfig — конфиг для мест, которые сами управляют TLS-dial'ом
// напрямую (eventsub), а не через net/http. В отличие от
// HTTPTransport, здесь ServerName обязателен — используется и для SNI,
// и для проверки имени в сертификате.
func DialTLSConfig(serverName string) *tls.Config {
	return &tls.Config{
		ServerName: serverName,
		RootCAs:    RootCAs(),
		MinVersion: tls.VersionTLS12,
	}
}
