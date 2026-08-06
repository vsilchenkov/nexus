package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"time"
)

// nodeMode — режим узла нагрузки. Каждый узел односценарный: он упражняет ровно
// одну фичу (§10.2), что даёт чистую per-mode статистику и понятные testcase'ы
// во вкладке Tests GitLab. RMQ-узлы (modeRMQ) живут в rmq.go отдельно.
type nodeMode string

const (
	modeSync      nodeMode = "sync"
	modeAsync     nodeMode = "async"
	modeDynURL    nodeMode = "dyn-url"
	modeAuthToken nodeMode = "auth-token"
	modeAuthBasic nodeMode = "auth-basic"
	modeRMQ       nodeMode = "rmq"
	// §83: async-узел с шаблоном ответа приёма. Отдельный режим, а не флаг у
	// modeAsync: per-mode статистика показывает цену шаблона прямо в отчёте,
	// рядом с обычным async на той же нагрузке.
	modeAck nodeMode = "ack"
)

// node — созданный через Web API узел нагрузки.
type node struct {
	path    string
	mode    nodeMode
	urlBase string // для modeDynURL — значение, передаваемое в ?url_base=
}

// planNodes распределяет httpNodes узлов по режимам согласно долям из flags.
// База долей — totalNodes (== f.Nodes, как у --ratio-rmq в rmq.go), что
// соответствует «доля узлов» из §10.2. RMQ-узлы вырезаются отдельно в main.
//
// Доли — НЕЗАВИСИМЫЕ корзины (а не вероятности по измерениям): сумма должна быть
// ≤ 1, остаток добивается plain-sync. При переполнении (сумма > httpNodes)
// лишние корзины усекаются — поэтому держи сумму ≤ 1 в вызывающем профиле.
func planNodes(httpNodes, totalNodes int, f flags) []nodeMode {
	modes := make([]nodeMode, 0, httpNodes)
	add := func(m nodeMode, ratio float64) {
		n := int(float64(totalNodes)*ratio + 0.5)
		for i := 0; i < n && len(modes) < httpNodes; i++ {
			modes = append(modes, m)
		}
	}
	add(modeAsync, f.RatioAsync)
	add(modeAck, f.RatioAck)
	add(modeDynURL, f.RatioDynamicURL)
	add(modeAuthToken, f.RatioAuthToken)
	add(modeAuthBasic, f.RatioAuthBasic)
	for len(modes) < httpNodes {
		modes = append(modes, modeSync)
	}
	return modes
}

// modeCounts агрегирует число узлов по режимам — для печати сводки.
func modeCounts(modes []nodeMode) map[nodeMode]int {
	out := make(map[nodeMode]int, len(modes))
	for _, m := range modes {
		out[m]++
	}
	return out
}

// nodeCreateBody строит тело POST /api/nodes для узла заданного режима.
// Имена полей — DTO CreateNodeRequest (internal/web/adapter/in/http/dto.go).
// Динамический контракт — internal/receiver/usecase/{urlresolver,auth_dynamic}.go:
//   - dyn-url: url_mode=from_request, target подаётся в ?url_base= при запросе;
//     url_allowed_hosts оставляем пустым => domain.HostAllowed = allow-all
//     (не завязываемся на каталог хостов §23);
//   - auth-token: token_from_request из заголовка Authorization (strip "Bearer ");
//   - auth-basic: basic_from_request читает Authorization: Basic <base64>.
func nodeCreateBody(mode nodeMode, path, targetURL, chTable string) map[string]any {
	body := map[string]any{
		"path":               path,
		"root_method":        "request",
		"target_url":         targetURL,
		"auth_type":          "none",
		"incoming_auth_type": "none",
		"timeout_ms":         30000,
		"clickhouse_table":   chTable,
	}
	switch mode {
	case modeAsync:
		body["root_method"] = "requestAsync"
	case modeAck:
		// §83: боевая форма шаблона (эхо максимального logId пакета). Тело
		// запроса под неё готовит ackRequestBody в main.go.
		body["root_method"] = "requestAsync"
		body["async_ack_spec"] = map[string]any{
			"version":      1,
			"content_type": "application/json",
			"body":         ackTemplate,
			"on_error":     "default",
		}
	case modeDynURL:
		body["url_mode"] = "from_request"
		body["url_param_name"] = "url_base"
	case modeAuthToken:
		body["auth_type"] = "token_from_request"
		body["auth_dynamic_source"] = "header"
		body["auth_dynamic_field"] = "Authorization"
		body["auth_dynamic_strip_prefix"] = "Bearer "
	case modeAuthBasic:
		body["auth_type"] = "basic_from_request"
	}
	return body
}

func (c *client) createNodes(ctx context.Context, modes []nodeMode, targetURL string) ([]node, error) {
	nodes := make([]node, 0, len(modes))
	for i, mode := range modes {
		path := fmt.Sprintf("loadtest/node-%d-%d", time.Now().UnixNano(), i)
		raw, _ := json.Marshal(nodeCreateBody(mode, path, targetURL, c.chTable))
		req, _ := http.NewRequestWithContext(ctx, "POST", c.baseWeb+"/api/nodes", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "nexus_session", Value: c.cookie})
		resp, err := c.hc.Do(req)
		if err != nil {
			return nodes, err
		}
		resp.Body.Close()
		if resp.StatusCode != 201 {
			return nodes, fmt.Errorf("create node %d (%s) status %d", i, mode, resp.StatusCode)
		}
		nd := node{path: path, mode: mode}
		if mode == modeDynURL {
			nd.urlBase = targetURL
		}
		nodes = append(nodes, nd)
	}
	return nodes, nil
}

// nodePaths извлекает пути узлов (для cleanup через deleteNodes).
func nodePaths(nodes []node) []string {
	paths := make([]string, len(nodes))
	for i, n := range nodes {
		paths[i] = n.path
	}
	return paths
}

// requestURL строит URL запроса к Receiver для узла: sync/async-эндпоинт,
// team-slug и (для dyn-url) служебный query-параметр url_base.
func requestURL(baseRecv, teamSlug string, n node) string {
	endpoint := "request"
	if n.mode == modeAsync || n.mode == modeAck {
		endpoint = "requestAsync"
	}
	u := baseRecv + "/api/v1/" + endpoint + "/"
	if teamSlug != "" {
		u += teamSlug + "/"
	}
	u += n.path
	if n.mode == modeDynURL {
		u += "?url_base=" + url.QueryEscape(n.urlBase)
	}
	return u
}

// randomHex возвращает случайную hex-строку длины n (для токенов/заголовков).
func randomHex(n int) string {
	const hexdigits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = hexdigits[rand.Intn(len(hexdigits))]
	}
	return string(b)
}

// addRandomHeaders добавляет 1–3 случайных X-Lt-* заголовка — упражняет
// проксирование/маскирование заголовков под нагрузкой (§10.2).
func addRandomHeaders(req *http.Request) {
	for i := range 1 + rand.Intn(3) {
		req.Header.Set(fmt.Sprintf("X-Lt-%d", i), randomHex(8))
	}
}
