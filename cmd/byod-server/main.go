package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	server "byod-server"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8787", "listen address")
	origin := flag.String("exam-origin", "https://exam.cs.ac.cn", "public exam origin")
	upstream := flag.String("upstream", "http://127.0.0.1:9000", "fixed exam upstream")
	examUpstreams := flag.String("exam-upstreams", os.Getenv("BYOD_EXAM_UPSTREAMS"), "JSON map of exam IDs to upstream base URLs")
	oidcIssuer := flag.String("oidc-issuer", os.Getenv("BYOD_OIDC_ISSUER"), "OIDC issuer URL")
	oidcClientID := flag.String("oidc-client-id", os.Getenv("BYOD_OIDC_CLIENT_ID"), "OIDC client ID")
	oidcClientSecret := flag.String("oidc-client-secret", os.Getenv("BYOD_OIDC_CLIENT_SECRET"), "OIDC client secret")
	oidcRedirect := flag.String("oidc-redirect-url", os.Getenv("BYOD_OIDC_REDIRECT_URL"), "OIDC callback URL")
	devAuth := flag.Bool("dev-auth", false, "enable development callback adapter")
	policyFile := flag.String("policy-file", os.Getenv("BYOD_POLICY_FILE"), "JSON policy document or exam-id map")
	flag.Parse()
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
	if *examUpstreams != "" {
		configured, parseErr := server.ParseExamUpstreams([]byte(*examUpstreams))
		if parseErr != nil {
			log.Fatal(parseErr)
		}
		service.ExamUpstreams = configured
	}
	service.DevAuth = *devAuth
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
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("BYOD server listening on http://%s", *listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
