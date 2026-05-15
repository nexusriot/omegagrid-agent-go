package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/bootstrap"
	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/httpapi"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	cfg := config.Load()

	svc, cleanup, err := bootstrap.New(cfg)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	defer cleanup()

	deps := httpapi.Deps{
		Cfg:       cfg,
		Agent:     svc.Agent,
		Memory:    svc.Memory,
		Skills:    svc.Skills,
		Chat:      svc.Chat,
		Scheduler: svc.Sched,
	}

	addr := fmt.Sprintf(":%d", cfg.BackendPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           httpapi.NewRouter(deps),
		ReadHeaderTimeout: 30 * time.Second,
	}

	go func() {
		log.Printf("gateway listening on %s (provider=%s, model=%s, skills-dir=%s)",
			addr, cfg.Provider, svc.Chat.Model(), cfg.SkillsDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
