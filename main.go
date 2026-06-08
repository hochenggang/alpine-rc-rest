package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	LoadToken()
	if apiToken == "" {
		log.Println("================================================================")
		log.Println(" [WARN] AUTHENTICATION DISABLED — DO NOT EXPOSE TO PUBLIC NETWORK")
		log.Println(" [WARN] set SERVICE_MANAGER_TOKEN env var and restart to enable")
		log.Println("================================================================")
	}

	mux := http.NewServeMux()
	// 项目管理
	mux.HandleFunc("GET /api/v1/project", listProjects)
	mux.HandleFunc("GET /api/v1/project/{name}", getProject)
	mux.HandleFunc("POST /api/v1/project", createProject)
	mux.HandleFunc("PUT /api/v1/project/{name}", updateProject)
	mux.HandleFunc("DELETE /api/v1/project/{name}", deleteProject)
	// 启动 / 停止 / 重启
	mux.HandleFunc("POST /api/v1/project/{name}/start", startProject)
	mux.HandleFunc("POST /api/v1/project/{name}/stop", stopProject)
	mux.HandleFunc("POST /api/v1/project/{name}/restart", restartProject)

	handler := corsMiddleware(authMiddleware(mux))

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("alpine-rc-rest listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("bye")
}

// corsMiddleware 为所有响应附加 CORS 头
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Token")
		w.Header().Set("Access-Control-Expose-Headers", "X-API-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
