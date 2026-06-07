package usecase

import (
	"net/http"
	"strconv"
)

// HeaderHops — служебный заголовок-счётчик переходов запроса через шину (§32).
// Receiver инкрементит его на каждом проходе; при достижении лимита запрос
// отклоняется как петля. Заголовок добавляется в обход allowlist узла
// (forward_headers) — он внутренний для шины и форвардится всегда.
const HeaderHops = "X-Nexus-Hops"

// nextHop читает входящий счётчик переходов из заголовков запроса, сверяет с
// лимитом и возвращает значение для исходящего запроса.
//
//   - maxHops <= 0 — защита выключена: loop=false всегда, out=0 (заголовок не
//     добавляется вызывающим кодом).
//   - отсутствующий или нечисловой/отрицательный заголовок трактуется как 0.
//   - incoming >= maxHops → loop=true (запрос отклоняется, наружу не уходит).
//   - иначе out = incoming+1 — это значение кладётся в исходящие заголовки.
func nextHop(h http.Header, maxHops int) (out int, loop bool) {
	if maxHops <= 0 {
		return 0, false
	}
	incoming := parseHops(h.Get(HeaderHops))
	if incoming >= maxHops {
		return 0, true
	}
	return incoming + 1, false
}

// parseHops разбирает значение заголовка X-Nexus-Hops. Пустое, нечисловое или
// отрицательное значение → 0 (счётчик стартует заново).
func parseHops(v string) int {
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
