// Command echosrv — настраиваемый тестовый сервис-получатель для ручного
// тестирования шины Nexus на стенде (см. docs/STAND_TESTING.md). Это
// ТОЛЬКО тестовая оснастка, не часть продакшн-сервисов.
//
// Принимает запросы любым методом (GET/POST/PUT/DELETE) и возвращает JSON с
// эхом: метод, путь, заголовки и тело входящего запроса. Это позволяет в
// логах ClickHouse и в ответе Request проверить, что Receiver/Sender довезли
// метод, заголовки и тело без искажений.
//
// Режимы авторизации (по path-префиксу) — чтобы протестировать узлы со всеми
// видами outgoing-auth, не поднимая несколько сервисов:
//
//	/noauth/*        — без проверки;
//	/basic/*         — требует Authorization: Basic (любая пара логин:пароль);
//	/token/*         — требует Authorization: Bearer <любой непустой токен>;
//	/echo/*          — алиас /noauth (просто эхо);
//	/status/<code>/* — всегда отвечает указанным HTTP-кодом (для проверки
//	                   ретраев/incomplete-метрик), напр. /status/500/x.
//
// Эхо-ответ включает заголовок Content-Type: application/json и заголовок
// X-Echo: 1 — чтобы проверить проброс заголовков получателя обратно клиенту (#6).
//
// Флаги:
//
//	-addr   адрес прослушивания (по умолчанию :9999)
//	-empty  если задан path-префикс /empty/* — вернуть пустое тело со статусом 200
//	        (проверка «пустое тело → пустой ответ», #6). Включён всегда.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", ":9999", "listen address")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", handler(logger))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("echosrv listening", slog.String("addr", *addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen failed", slog.Any("err", err))
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	logger.Info("echosrv stopped")
}

type echoResponse struct {
	OK      bool                `json:"ok"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
}

func handler(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 5<<20))
		_ = r.Body.Close()

		seg := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
		mode := ""
		if len(seg) > 0 {
			mode = seg[0]
		}

		logger.Info("request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("body_len", len(body)))

		switch mode {
		case "basic":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
				unauthorized(w, "basic auth required")
				return
			}
		case "token":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				unauthorized(w, "bearer token required")
				return
			}
		case "status":
			code := http.StatusOK
			if len(seg) > 1 {
				if c, err := strconv.Atoi(seg[1]); err == nil {
					code = c
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Echo", "1")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"forced_status":` + strconv.Itoa(code) + `}`))
			return
		case "empty":
			// Пустое тело со статусом 200 — проверка #6 «пустое → пустое».
			w.Header().Set("X-Echo", "1")
			w.WriteHeader(http.StatusOK)
			return
		}

		resp := echoResponse{
			OK:      true,
			Method:  r.Method,
			Path:    r.URL.Path,
			Query:   r.URL.RawQuery,
			Headers: r.Header,
			Body:    string(body),
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Echo", "1")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}
