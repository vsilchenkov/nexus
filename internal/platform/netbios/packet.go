// Package netbios — запрос NetBIOS Node Status (NBSTAT, RFC 1002 §4.2.17–18)
// поверх UDP/137 (§106.2).
//
// Нужен резолверу имени клиента (platform/rdns) как второй этап после
// пустого PTR: Windows-хосты домена без обратной DNS-записи отвечают на
// NBSTAT таблицей своих NetBIOS-имён, как на `nbtstat -A <ip>`. Пакет
// состоит из кодека (этот файл, без I/O) и UDP-клиента (client.go).
package netbios

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// NodeStatusRequestLen — длина запроса NBSTAT в байтах.
	NodeStatusRequestLen = 50
	// SuffixWorkstation — суффикс <00>: служба рабочей станции, под ним
	// хост регистрирует своё имя компьютера.
	SuffixWorkstation byte = 0x00

	typeNBSTAT       = 0x0021
	classIN          = 0x0001
	flagResponse     = 0x8000 // бит R заголовка
	rcodeMask        = 0x000F
	flagGroup        = 0x8000 // бит G в NAME_FLAGS
	headerLen        = 12
	rrFixedLen       = 10 // TYPE(2) + CLASS(2) + TTL(4) + RDLENGTH(2)
	nodeNameEntryLen = 18 // 15 байт имени + суффикс + 2 байта флагов
	netbiosNameLen   = 15
	encodedNameBytes = 32 // имя из 16 байт, каждый полубайт → буква
	labelPointerMask = 0xC0
)

// Name — одна запись таблицы имён из ответа NBSTAT.
type Name struct {
	// Name — имя без завершающих пробелов и NUL, как есть (байты OEM).
	Name   string
	Suffix byte
	// Group — имя группы (домен, рабочая группа), а не уникальное имя хоста.
	Group bool
	Flags uint16
}

// Ошибки разбора ответа. Все — повод считать ответ негодным, а хост —
// безымянным; клиент различает только ErrTransactionID (чужая датаграмма,
// читать дальше).
var (
	ErrShortPacket   = errors.New("netbios: packet too short")
	ErrTransactionID = errors.New("netbios: transaction id mismatch")
	ErrNotResponse   = errors.New("netbios: not a response")
	ErrRCode         = errors.New("netbios: error rcode")
	ErrNoAnswer      = errors.New("netbios: no answer record")
	ErrBadRecord     = errors.New("netbios: malformed answer record")
)

// BuildNodeStatusRequest собирает 50-байтный запрос NBSTAT к имени «*»:
// заголовок (один вопрос, без флагов — одноадресный запрос), имя «*» в
// first-level-кодировке (0x20, "CK" + "A"×30, 0x00), QTYPE NBSTAT, QCLASS IN.
func BuildNodeStatusRequest(txID uint16) []byte {
	b := make([]byte, 0, NodeStatusRequestLen)
	b = binary.BigEndian.AppendUint16(b, txID)
	b = binary.BigEndian.AppendUint16(b, 0) // флаги
	b = binary.BigEndian.AppendUint16(b, 1) // QDCOUNT
	b = append(b, 0, 0, 0, 0, 0, 0)         // AN, NS, AR
	b = append(b, encodedNameBytes)
	b = append(b, 'C', 'K') // '*' = 0x2A → 'A'+0x2, 'A'+0xA
	b = append(b, bytes.Repeat([]byte{'A'}, encodedNameBytes-2)...)
	b = append(b, 0)
	b = binary.BigEndian.AppendUint16(b, typeNBSTAT)
	return binary.BigEndian.AppendUint16(b, classIN)
}

// ParseNodeStatusResponse разбирает ответ NBSTAT и возвращает таблицу имён.
// Проверяет номер транзакции, бит ответа, RCODE, границы всех полей. Вопросы
// (если ответчик их повторил) и сжатые имена пропускаются; блок статистики
// после таблицы имён не читается.
func ParseNodeStatusResponse(txID uint16, pkt []byte) ([]Name, error) {
	qd, err := parseHeader(txID, pkt)
	if err != nil {
		return nil, err
	}
	off := headerLen
	for range qd {
		if off, err = skipName(pkt, off); err != nil {
			return nil, err
		}
		off += 4 // QTYPE + QCLASS
		if off > len(pkt) {
			return nil, ErrShortPacket
		}
	}
	rdata, err := parseAnswerRR(pkt, off)
	if err != nil {
		return nil, err
	}
	return parseNodeNames(rdata)
}

// parseHeader проверяет заголовок и возвращает число вопросов.
func parseHeader(txID uint16, pkt []byte) (int, error) {
	if len(pkt) < headerLen {
		return 0, ErrShortPacket
	}
	if got := binary.BigEndian.Uint16(pkt[0:2]); got != txID {
		return 0, fmt.Errorf("%w: got %d, want %d", ErrTransactionID, got, txID)
	}
	flags := binary.BigEndian.Uint16(pkt[2:4])
	if flags&flagResponse == 0 {
		return 0, ErrNotResponse
	}
	if rc := flags & rcodeMask; rc != 0 {
		return 0, fmt.Errorf("%w: %d", ErrRCode, rc)
	}
	if binary.BigEndian.Uint16(pkt[6:8]) == 0 {
		return 0, ErrNoAnswer
	}
	return int(binary.BigEndian.Uint16(pkt[4:6])), nil
}

// skipName пропускает имя в метке-кодировке (длина + байты, до нулевой
// метки) или указатель сжатия (два байта 0xC0xx) и возвращает смещение
// следующего поля. Смещение строго растёт, поэтому цикл конечен.
func skipName(pkt []byte, off int) (int, error) {
	for {
		if off >= len(pkt) {
			return 0, ErrShortPacket
		}
		l := int(pkt[off])
		switch {
		case l == 0:
			return off + 1, nil
		case l&labelPointerMask == labelPointerMask:
			if off+2 > len(pkt) {
				return 0, ErrShortPacket
			}
			return off + 2, nil
		case l&labelPointerMask != 0:
			return 0, fmt.Errorf("%w: label byte %#x", ErrBadRecord, l)
		}
		off += 1 + l
	}
}

// parseAnswerRR разбирает запись ответа и возвращает её RDATA.
func parseAnswerRR(pkt []byte, off int) ([]byte, error) {
	off, err := skipName(pkt, off)
	if err != nil {
		return nil, err
	}
	if off+rrFixedLen > len(pkt) {
		return nil, ErrShortPacket
	}
	if typ := binary.BigEndian.Uint16(pkt[off : off+2]); typ != typeNBSTAT {
		return nil, fmt.Errorf("%w: type %#x", ErrBadRecord, typ)
	}
	rdLen := int(binary.BigEndian.Uint16(pkt[off+8 : off+10]))
	start := off + rrFixedLen
	if start+rdLen > len(pkt) {
		return nil, ErrShortPacket
	}
	return pkt[start : start+rdLen], nil
}

// parseNodeNames разбирает NUM_NAMES и таблицу имён из RDATA.
func parseNodeNames(rdata []byte) ([]Name, error) {
	if len(rdata) < 1 {
		return nil, ErrShortPacket
	}
	n := int(rdata[0])
	if 1+n*nodeNameEntryLen > len(rdata) {
		return nil, ErrShortPacket
	}
	names := make([]Name, 0, n)
	for i := range n {
		e := rdata[1+i*nodeNameEntryLen : 1+(i+1)*nodeNameEntryLen]
		flags := binary.BigEndian.Uint16(e[16:18])
		names = append(names, Name{
			Name:   string(bytes.TrimRight(e[:netbiosNameLen], " \x00")),
			Suffix: e[netbiosNameLen],
			Group:  flags&flagGroup != 0,
			Flags:  flags,
		})
	}
	return names, nil
}

// WorkstationName выбирает имя компьютера: первое уникальное (не групповое)
// имя с суффиксом <00>, длиной до 15 символов из [A-Za-z0-9_-]. Имя с любым
// другим байтом (OEM-кодировка, пунктуация) пропускается — берётся следующее
// подходящее. Групповое имя домена и имя службы файлов <20> не подходят.
func WorkstationName(names []Name) (string, bool) {
	for _, n := range names {
		if n.Group || n.Suffix != SuffixWorkstation || !validName(n.Name) {
			continue
		}
		return n.Name, true
	}
	return "", false
}

func validName(s string) bool {
	if s == "" || len(s) > netbiosNameLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		ok := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}
