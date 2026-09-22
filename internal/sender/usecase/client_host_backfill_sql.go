package usecase

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"

	"nexus/internal/domain"
)

// clientHostPattern — имя клиента, допустимое в литерале мутации (§106.3):
// DNS-имя или NetBIOS-имя с суффиксом. Кавычек, пробелов и обратных слешей в
// нём быть не может, поэтому экранирование литералу не нужно. Неизменяемое
// значение — как ensureTableNamePattern в platform/clickhouse.
var clientHostPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)

// buildClientHostMutation собирает одну асинхронную мутацию на таблицу:
//
//	ALTER TABLE <t> UPDATE client_host = transform(IP, [ip…], [host…], client_host)
//	WHERE client_host = '' AND IP IN (ip…)
//
// IP берётся, только если его разбирает net.ParseIP, имя — только если оно
// проходит clientHostPattern; пары сортируются по IP (детерминированный SQL).
// Непустой client_host мутация не трогает никогда. ok=false — подставлять
// нечего (или имя таблицы негодно), мутацию не ставить.
func buildClientHostMutation(table string, hosts map[string]string) (stmt string, pairs int, ok bool) {
	if !domain.IsValidCHTableName(table) {
		return "", 0, false
	}
	ips := make([]string, 0, len(hosts))
	for ip, host := range hosts {
		if net.ParseIP(ip) == nil || !clientHostPattern.MatchString(host) {
			continue
		}
		ips = append(ips, ip)
	}
	if len(ips) == 0 {
		return "", 0, false
	}
	slices.Sort(ips)
	names := make([]string, len(ips))
	for i, ip := range ips {
		names[i] = hosts[ip]
	}
	ipList, nameList := quoteList(ips), quoteList(names)
	stmt = fmt.Sprintf(
		"ALTER TABLE %s UPDATE client_host = transform(IP, [%s], [%s], client_host) WHERE client_host = '' AND IP IN (%s)",
		table, ipList, nameList, ipList)
	return stmt, len(ips), true
}

// quoteList — 'a', 'b', … для значений, заранее проверенных на отсутствие кавычек.
func quoteList(vals []string) string {
	var sb strings.Builder
	for i, v := range vals {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteByte('\'')
		sb.WriteString(v)
		sb.WriteByte('\'')
	}
	return sb.String()
}
