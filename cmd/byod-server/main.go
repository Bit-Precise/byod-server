package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	server "byod-server"
)

func main() {
	configureLogging()
	listen := flag.String("listen", "127.0.0.1:8787", "listen address")
	role := flag.String("role", "all", "runtime role: control, data, or all (local testing)")
	origin := flag.String("exam-origin", "https://exam.cs.ac.cn", "public exam origin")
	upstream := flag.String("upstream", "http://127.0.0.1:9000", "fixed exam upstream")
	databaseURL := flag.String("database-url", os.Getenv("BYOD_DATABASE_URL"), "PostgreSQL connection URL for exam metadata")
	adminToken := flag.String("admin-token", os.Getenv("BYOD_ADMIN_TOKEN"), "administrator API token")
	adminEmails := flag.String("admin-emails", os.Getenv("BYOD_ADMIN_EMAILS"), "comma-separated verified OIDC emails allowed to bootstrap administrators")
	oidcIssuer := flag.String("oidc-issuer", os.Getenv("BYOD_OIDC_ISSUER"), "OIDC issuer URL")
	oidcClientID := flag.String("oidc-client-id", os.Getenv("BYOD_OIDC_CLIENT_ID"), "OIDC client ID")
	oidcClientSecret := flag.String("oidc-client-secret", os.Getenv("BYOD_OIDC_CLIENT_SECRET"), "OIDC client secret")
	oidcRedirect := flag.String("oidc-redirect-url", os.Getenv("BYOD_OIDC_REDIRECT_URL"), "OIDC callback URL")
	devAuth := flag.Bool("dev-auth", false, "enable development callback adapter")
	policyFile := flag.String("policy-file", os.Getenv("BYOD_POLICY_FILE"), "JSON policy document or exam-id map")
	migrate := flag.Bool("migrate", false, "apply PostgreSQL schema migrations and exit")
	flag.Parse()
	if *role != "control" && *role != "data" && *role != "all" {
		log.Fatal("--role must be control, data, or all")
	}
	if *migrate {
		if *databaseURL == "" {
			log.Fatal("--migrate requires BYOD_DATABASE_URL or --database-url")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := server.MigratePostgres(ctx, *databaseURL); err != nil {
			log.Fatal(err)
		}
		return
	}
	secretValue := os.Getenv("BYOD_POLICY_SECRET")
	if *role == "data" && (*databaseURL == "" || (*oidcIssuer == "" && !*devAuth)) {
		log.Fatal("--role=data requires a database and OIDC issuer (or --dev-auth)")
	}
	if secretValue == "" && !*devAuth && *role != "data" {
		log.Fatal("BYOD_POLICY_SECRET is required unless --dev-auth is enabled")
	}
	secret := []byte(secretValue)
	if len(secret) == 0 {
		secret = []byte("development-only-secret")
	}
	service, err := server.NewService(*origin, *upstream, secret)
	if err != nil {
		log.Fatal(err)
	}
	service.DevAuth = *devAuth
	service.Role = *role
	service.IdentityIssuer = *oidcIssuer
	if *role == "data" {
		service.PolicySecret = nil
	} else {
		service.AdminToken = *adminToken
		for _, email := range strings.Split(*adminEmails, ",") {
			if normalized, err := server.NormalizeEmailForConfig(email); err == nil {
				service.AdminEmails[normalized] = true
			}
		}
	}
	if *databaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		store, storeErr := server.OpenPostgresStore(ctx, *databaseURL)
		cancel()
		if storeErr != nil {
			log.Fatal(storeErr)
		}
		service.ExamStore = store
		for email := range service.AdminEmails {
			store.AdminEmails[email] = true
		}
		defer store.Close()
	}
	if *policyFile != "" {
		data, readErr := os.ReadFile(*policyFile)
		if readErr != nil {
			log.Fatal(readErr)
		}
		overrides, parseErr := server.ParsePolicyOverrides(data)
		if parseErr != nil {
			log.Fatal(parseErr)
		}
		service.PolicyOverrides = overrides
	}
	if *oidcIssuer != "" && *role != "data" {
		authenticator, authErr := server.NewOIDCAuthenticator(context.Background(), *oidcIssuer, *oidcClientID, *oidcClientSecret, *oidcRedirect)
		if authErr != nil {
			log.Fatal(authErr)
		}
		service.OIDC = authenticator
	}
	// Run on startup as well as periodically: old durable sessions must expire
	// even when their browser never sends another request after disconnecting.
	expiryContext, stopExpiry := context.WithCancel(context.Background())
	expiryDone := make(chan struct{})
	go func() { defer close(expiryDone); service.RunSessionExpiry(expiryContext) }()
	defer func() { stopExpiry(); <-expiryDone }()
	server := &http.Server{Addr: *listen, Handler: service, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		stopExpiry()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		service.CloseTunnels()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("BYOD %s listening on http://%s", *role, *listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func configureLogging() {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BYOD_LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	options := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("BYOD_LOG_FORMAT")), "text") {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, options)))
		return
	}
	// JSON is the default because Kubernetes log collectors can index fields
	// such as request_id, exam_id, status and duration_ms directly.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, options)))
}
