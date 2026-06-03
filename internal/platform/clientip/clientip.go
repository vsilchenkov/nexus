// Package clientip нормализует IP-адрес клиента к IPv4-представлению, где это
// возможно (§4 ТЗ: в журнале аудита и логах фиксируем ip4, а не ip6).
//
// Зачем: при обращении к сервису по localhost Go/Gin часто отдают IPv6-форму
// (`::1`, либо IPv4-mapped `::ffff:10.0.0.1`). В аудите и ClickHouse-логах это
// читается хуже, чем привычный IPv4. NormalizeIPv4 приводит такие адреса к
// IPv4; «настоящие» IPv6-адреса и не-IP-строки возвращаются как есть.
package clientip

import "net"

// NormalizeIPv4 возвращает IPv4-представление адреса, если оно есть:
//   - IPv4-mapped IPv6 (`::ffff:1.2.3.4`) → `1.2.3.4`;
//   - IPv6 loopback (`::1`) → `127.0.0.1`;
//   - обычный IPv4 — без изменений;
//   - прочие IPv6 — каноничная форma (без изменения семантики);
//   - не-IP строка (или пустая) — возвращается как есть.
func NormalizeIPv4(s string) string {
	if s == "" {
		return s
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return s
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	if ip.Equal(net.IPv6loopback) {
		return "127.0.0.1"
	}
	return ip.String()
}
