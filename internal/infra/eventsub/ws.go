package eventsub

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Минимальный WebSocket-клиент по RFC 6455 — перенос из спайка,
// проверенного против реального wss://eventsub.wss.twitch.tv.
// Достаточен для чтения текстовых сообщений и прозрачной обработки
// control-фреймов (ping/pong/close), не претендует на универсальность.
// Собирает фрагментированные (multi-frame) сообщения по FIN-биту (см.
// readMessage) — EventSub-пейлоады почти всегда укладываются в один
// фрейм, но раньше FIN игнорировался НАМЕРЕННО в расчёте на это; после
// разбора реального протокольного риска решили не полагаться на
// эмпирическое наблюдение как на гарантию.

const wsMagicGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	opText         = 0x1
	opContinuation = 0x0
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xa
)

// pongWriteTimeout — дедлайн на отправку Pong-ответа. Если сеть
// настолько плоха, что даже несколько байт Pong не уходят за это
// время, соединение всё равно не жилец — лучше сразу считать его
// оборванным, чем зависнуть в writeFrame навсегда.
const pongWriteTimeout = 10 * time.Second

// performHandshake выполняет HTTP Upgrade к WebSocket поверх уже
// открытого соединения (обычно TLS).
func performHandshake(conn io.Writer, br *bufio.Reader, host, path string) error {
	key, err := randomWebSocketKey()
	if err != nil {
		return err
	}

	req, err := http.NewRequest("GET", "https://"+host+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Sec-WebSocket-Version", "13")

	if err := req.Write(conn); err != nil {
		return err
	}

	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != computeAcceptKey(key) {
		return fmt.Errorf("Sec-WebSocket-Accept mismatch")
	}

	return nil
}

type wsFrame struct {
	opcode  byte
	fin     bool
	payload []byte
}

func readFrame(r *bufio.Reader) (wsFrame, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return wsFrame{}, err
	}

	fin := header[0]&0x80 != 0
	opcode := header[0] & 0x0f

	if header[1]&0x80 != 0 {
		return wsFrame{}, errors.New("сервер прислал замаскированный фрейм")
	}

	length := int64(header[1] & 0x7f)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return wsFrame{}, err
		}
		length = int64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(r, ext); err != nil {
			return wsFrame{}, err
		}
		length = int64(binary.BigEndian.Uint64(ext))
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return wsFrame{}, err
	}

	return wsFrame{opcode: opcode, fin: fin, payload: payload}, nil
}

func writeFrame(w io.Writer, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}

	length := len(payload)
	switch {
	case length <= 125:
		header = append(header, 0x80|byte(length))
	case length <= 65535:
		header = append(header, 0x80|126)
		ext := make([]byte, 2)
		binary.BigEndian.PutUint16(ext, uint16(length))
		header = append(header, ext...)
	default:
		header = append(header, 0x80|127)
		ext := make([]byte, 8)
		binary.BigEndian.PutUint64(ext, uint64(length))
		header = append(header, ext...)
	}

	maskKey := make([]byte, 4)
	if _, err := rand.Read(maskKey); err != nil {
		return err
	}
	header = append(header, maskKey...)

	masked := make([]byte, length)
	for i, b := range payload {
		masked[i] = b ^ maskKey[i%4]
	}

	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(masked)
	return err
}

// readMessage читает фреймы, пока не соберёт полное текстовое
// сообщение, прозрачно отвечая Pong на Ping по пути и склеивая
// фрагментированные (multi-frame) сообщения по FIN-биту. Control-фреймы
// (ping/pong/close) по RFC 6455 §5.4 разрешено присылать МЕЖДУ
// фрагментами одного сообщения — они никогда сами не фрагментируются
// (FIN у них всегда 1), и цикл ниже не путает их с продолжением данных
// (fragmented/buf трогают только opText/opContinuation). conn —
// net.Conn, а не просто io.Writer: нужен SetWriteDeadline на случай,
// если сеть жива ровно настолько, чтобы TCP-запись не вернула ошибку
// сразу, но недостаточно, чтобы данные реально ушли.
func readMessage(r *bufio.Reader, conn net.Conn) (string, error) {
	var (
		fragmented bool   // собираем ли сейчас multi-frame сообщение
		buf        []byte // уже полученные фрагменты текущего сообщения
	)

	for {
		f, err := readFrame(r)
		if err != nil {
			return "", err
		}

		switch f.opcode {
		case opText:
			if fragmented {
				return "", errors.New("новый текстовый фрейм посреди незавершённого сообщения")
			}
			if f.fin {
				return string(f.payload), nil
			}
			fragmented = true
			buf = append(buf, f.payload...)

		case opContinuation:
			if !fragmented {
				return "", errors.New("continuation-фрейм без начатого сообщения")
			}
			buf = append(buf, f.payload...)
			if f.fin {
				return string(buf), nil
			}

		case opPing:
			if err := conn.SetWriteDeadline(time.Now().Add(pongWriteTimeout)); err != nil {
				return "", fmt.Errorf("set pong deadline: %v", err)
			}
			if err := writeFrame(conn, opPong, f.payload); err != nil {
				return "", fmt.Errorf("send pong: %v", err)
			}
		case opPong:
			// сервер не обязан слать pong в ответ на наш — игнорируем.
		case opClose:
			// Отвечаем Close-фреймом из вежливости (RFC 6455 §5.5.1) —
			// best-effort, ошибку игнорируем: соединение и так сейчас
			// закрываем.
			_ = conn.SetWriteDeadline(time.Now().Add(pongWriteTimeout))
			_ = writeFrame(conn, opClose, nil)
			return "", fmt.Errorf("сервер закрыл соединение")
		default:
			return "", fmt.Errorf("неожиданный opcode: %d", f.opcode)
		}
	}
}

func randomWebSocketKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func computeAcceptKey(key string) string {
	h := sha1.New()
	h.Write([]byte(key + wsMagicGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
