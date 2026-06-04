package usecase

import (
	"net/url"
	"strings"

	"nexus/internal/domain"
)

// checkSelfReference отклоняет узел, чей target_url указывает на собственный
// ingress шины (§32.2) — частая ошибка конфигурации, ведущая к зацикливанию.
//
// Проверка best-effort и работает только когда:
//   - задан список своих authority (selfIngressHosts непустой);
//   - url_mode=static (у from_request статического URL нет — его покрывает
//     hop-счётчик §32.1).
//
// Loopback (localhost/127.0.0.1/::1) намеренно НЕ блокируется безусловно —
// блокируется только точное совпадение с собственным ingress.
func (u *NodeUsecase) checkSelfReference(n *domain.Node) error {
	if len(u.selfIngressHosts) == 0 || n.URLMode != domain.URLModeStatic {
		return nil
	}
	if isSelfReferenceTarget(n.TargetURL, u.selfIngressHosts) {
		return domain.ErrNodeTargetURLSelfReference
	}
	return nil
}

// isSelfReferenceTarget — true, если target_url ведёт на входной маршрут шины
// (/v1/request* или /api/v1/request*) на одном из «своих» authority.
func isSelfReferenceTarget(targetURL string, selfHosts []string) bool {
	parsed, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil || parsed.Host == "" {
		return false
	}
	if !isIngressPath(parsed.Path) {
		return false
	}
	return hostMatchesSelf(parsed, selfHosts)
}

// isIngressPath — путь указывает на входной endpoint Receiver-а. Префикс
// /v1/request покрывает и /v1/requestAsync; аналогично для /api/v1/.
func isIngressPath(path string) bool {
	return strings.HasPrefix(path, "/v1/request") || strings.HasPrefix(path, "/api/v1/request")
}

// hostMatchesSelf сравнивает authority цели со списком своих. Элемент с портом
// (есть ':') сверяется как полное authority (host:port), без порта — только по
// хосту (порт цели игнорируется).
func hostMatchesSelf(target *url.URL, selfHosts []string) bool {
	for _, self := range selfHosts {
		self = strings.TrimSpace(self)
		if self == "" {
			continue
		}
		if strings.Contains(self, ":") {
			if strings.EqualFold(target.Host, self) {
				return true
			}
			continue
		}
		if strings.EqualFold(target.Hostname(), self) {
			return true
		}
	}
	return false
}
