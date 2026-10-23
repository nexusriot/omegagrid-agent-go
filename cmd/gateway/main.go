package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
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

	deps := httpapi.Deps{
		Cfg:          cfg,
		Agent:        svc.Agent,
		Memory:       svc.Memory,
		Skills:       svc.Skills,
		Chat:         svc.Chat,
		Scheduler:    svc.Sched,
		ToolProvider: svc.ToolProvider,
	}

	// baseCtx parents every request context; cancelling it on SIGTERM tells
	// in-flight handlers (long agent runs, SSE streams) to wind down instead
	// of running on until Shutdown's deadline cuts the connection under them.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()

	addr := fmt.Sprintf(":%d", cfg.BackendPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           httpapi.NewRouter(deps),
		ReadHeaderTimeout: 30 * time.Second,
		// Bounds a slow-loris request body. No WriteTimeout: SSE streams and
		// long agent runs legitimately keep the response open for minutes.
		ReadTimeout:    60 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 64 << 10,
		BaseContext:    func(net.Listener) context.Context { return baseCtx },
	}

	// Only the gateway runs scheduled tasks; bootstrap.New leaves the runner
	// stopped so CLI local mode doesn't start a competing one.
	svc.Runner.Start()

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("gateway listening on %s (provider=%s, model=%s, skills-dir=%s)",
			addr, cfg.Provider, svc.Chat.Model(), cfg.SkillsDir)
		// log.Fatalf here skipped every deferred cleanup (scheduler stop, DB
		// close); report the error to main instead.
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	exitCode := 0
	select {
	case sig := <-stop:
		log.Printf("shutting down (%s)", sig)
	case err := <-serveErr:
		log.Printf("listen: %v", err)
		exitCode = 1
	}
	signal.Stop(stop)

	// Order: stop accepting and drain HTTP (cancelling in-flight request
	// contexts), then cleanup — which stops the scheduler (bounded wait) before
	// closing the databases.
	cancelBase()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("http shutdown: %v; closing remaining connections", err)
		_ = srv.Close()
	}
	cancel()
	cleanup()
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
