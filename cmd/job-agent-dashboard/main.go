package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed web/*
var dashboardFiles embed.FS

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("job-agent-dashboard", flag.ContinueOnError)
	listen := flags.String("listen", envOr("JOB_AGENT_DASHBOARD_LISTEN", "127.0.0.1:8081"), "dashboard listen address")
	upstream := flags.String("upstream", envOr("JOB_AGENT_API_URL", "http://127.0.0.1:8080"), "job-agent API base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	handler, err := newDashboardHandler(*upstream)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	result := make(chan error, 1)
	go func() {
		log.Printf("job-agent dashboard listening on http://%s", server.Addr)
		result <- server.ListenAndServe()
	}()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func newDashboardHandler(upstreamValue string) (http.Handler, error) {
	upstream, err := url.Parse(upstreamValue)
	if err != nil {
		return nil, fmt.Errorf("parse API upstream: %w", err)
	}
	if (upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.Host == "" || upstream.User != nil || upstream.RawQuery != "" || upstream.Fragment != "" || (upstream.Path != "" && upstream.Path != "/") {
		return nil, errors.New("API upstream must be an http(s) origin without credentials, path, query or fragment")
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.ErrorHandler = func(response http.ResponseWriter, _ *http.Request, proxyErr error) {
		log.Printf("dashboard API proxy: %v", proxyErr)
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusBadGateway)
		_, _ = response.Write([]byte(`{"error":"job-agent API is unavailable"}`))
	}
	webRoot, err := fs.Sub(dashboardFiles, "web")
	if err != nil {
		return nil, fmt.Errorf("open embedded dashboard: %w", err)
	}
	static := http.FileServer(http.FS(webRoot))
	mux := http.NewServeMux()
	mux.Handle("/api/", proxy)
	mux.Handle("/healthz", proxy)
	mux.Handle("/readyz", proxy)
	mux.HandleFunc("GET /dashboard-healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/", static)
	return securityHeaders(mux), nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
