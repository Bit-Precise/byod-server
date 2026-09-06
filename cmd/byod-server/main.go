package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	server "byod-server"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8787", "listen address")
	tunnelListen := flag.String("tunnel-listen", "127.0.0.1:8788", "raw BYOD tunnel listen address; empty disables the data plane")
	tunnelEndpoint := flag.String("tunnel-endpoint", os.Getenv("BYOD_TUNNEL_ENDPOINT"), "public BYOD tunnel host:port advertised to browsers")
	origin := flag.String("exam-origin", "https://exam.cs.ac.cn", "public exam origin")
	upstream := flag.String("upstream", "http://127.0.0.1:9000", "fixed exam upstream")
	databaseURL := flag.String("database-url", os.Getenv("BYOD_DATABASE_URL"), "PostgreSQL connection URL for exam metadata")
	adminToken := flag.String("admin-token", os.Getenv("BYOD_ADMIN_TOKEN"), "administrator API token")
	oidcIssuer := flag.String("oidc-issuer", os.Getenv("BYOD_OIDC_ISSUER"), "OIDC issuer URL")
	oidcClientID := flag.String("oidc-client-id", os.Getenv("BYOD_OIDC_CLIENT_ID"), "OIDC client ID")
	oidcClientSecret := flag.String("oidc-client-secret", os.Getenv("BYOD_OIDC_CLIENT_SECRET"), "OIDC client secret")
	oidcRedirect := flag.String("oidc-redirect-url", os.Getenv("BYOD_OIDC_REDIRECT_URL"), "OIDC callback URL")
	devAuth := flag.Bool("dev-auth", false, "enable development callback adapter")
	policyFile := flag.String("policy-file", os.Getenv("BYOD_POLICY_FILE"), "JSON policy document or exam-id map")
	migrate := flag.Bool("migrate", false, "apply PostgreSQL schema migrations and exit")
	flag.Parse()
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
	if secretValue == "" && !*devAuth {
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
	if *tunnelEndpoint != "" {
		service.TunnelEndpoint = *tunnelEndpoint
	}
	service.AdminToken = *adminToken
	if *databaseURL != "" {
		store, storeErr := server.OpenPostgresStore(context.Background(), *databaseURL)
		if storeErr != nil {
			log.Fatal(storeErr)
		}
		service.ExamStore = store
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
	if *oidcIssuer != "" {
		authenticator, authErr := server.NewOIDCAuthenticator(context.Background(), *oidcIssuer, *oidcClientID, *oidcClientSecret, *oidcRedirect)
		if authErr != nil {
			log.Fatal(authErr)
		}
		service.OIDC = authenticator
	}
	server := &http.Server{Addr: *listen, Handler: service, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	var tunnelListener net.Listener
	if *tunnelListen != "" {
		var listenErr error
		tunnelListener, listenErr = net.Listen("tcp", *tunnelListen)
		if listenErr != nil {
			log.Fatal(listenErr)
		}
		log.Printf("BYOD tunnel listening on tcp://%s", *tunnelListen)
		go func() {
			for {
				conn, acceptErr := tunnelListener.Accept()
				if acceptErr != nil {
					if ne, ok := acceptErr.(net.Error); ok && ne.Temporary() {
						time.Sleep(50 * time.Millisecond)
						continue
					}
					return
				}
				go service.ServeTunnel(context.Background(), conn)
			}
		}()
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if tunnelListener != nil {
			_ = tunnelListener.Close()
			tunnelListener = nil
		}
		_ = server.Shutdown(ctx)
	}()
	log.Printf("BYOD server listening on http://%s", *listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
