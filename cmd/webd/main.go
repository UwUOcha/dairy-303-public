// webd — публичный сайт. Доступ к расписанию только через сокет raspd.
package main

import (
	"context"
	"errors"
	"flag"
	"github.com/UwUOcha/dairy-303-public/internal/admin"
	"github.com/UwUOcha/dairy-303-public/internal/config"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/web"
)

func main() {
	if err := profile.Init(); err != nil {
		slog.Error("профиль установки", "ошибка", err)
		os.Exit(1)
	}
	demo := flag.Bool("demo", false, "Явный демонстрационный режим без подключения к расписанию")
	flag.Parse()
	addr := os.Getenv("WEB_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8306"
	}
	socket := os.Getenv("RASP_SOCKET")
	if socket == "" {
		socket = "/run/rasp/api.sock"
	}
	// Точный внешний HTTPS origin для проверки запросов авторизации и OG.
	publicURL := os.Getenv("WEB_PUBLIC_URL")
	if publicURL == "" {
		publicURL = profile.Current().PublicURL
	}
	if !*demo && profile.Current().Access == "allowlist" && publicURL == "" {
		slog.Error("public_url is required for authenticated website")
		os.Exit(1)
	}
	log := slog.Default()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	handler := web.New(socket, *demo, log, publicURL)
	if os.Getenv("ADMIN_ENABLED") == "true" {
		if *demo || publicURL == "" {
			log.Error("admin requires a real installation and public_url")
			os.Exit(1)
		}
		p := profile.Current()
		if p.AdminTG == "" && p.AdminVK == "" {
			log.Error("admin requires admin_tg or admin_vk in the shared profile")
			os.Exit(1)
		}
		cfg, err := config.LoadAdmin()
		if err != nil {
			log.Error("admin configuration", "error", err)
			os.Exit(1)
		}
		cfg.BasePath = "/admin"
		history, err := admin.OpenHistory(ctx, cfg.DBPath, cfg.Retention)
		if err != nil {
			log.Error("admin history", "error", err)
			os.Exit(1)
		}
		defer history.Close()
		panel, err := admin.New(cfg, log, history)
		if err != nil {
			log.Error("admin", "error", err)
			os.Exit(1)
		}
		go panel.Run(ctx)
		handler = web.WithAdmin(handler, panel.SiteHandler(), socket, cfg, log)
	}
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	errch := make(chan error, 1)
	go func() {
		log.Info("сайт слушает", "url", "http://"+addr, "demo", *demo)
		errch <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err := <-errch:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("webd", "ошибка", err)
			os.Exit(1)
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Error("остановка сайта", "ошибка", err)
	}
}
