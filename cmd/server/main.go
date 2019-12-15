package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/newrelic/k8s-webhook/src/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"go.uber.org/zap/zapcore"

	"github.com/fsnotify/fsnotify"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/zap"

	"github.com/newrelic/k8s-webhook/src/server"
)

const (
	envVarPrefix   = "NEW_RELIC_K8S_WEBHOOK"
	defaultTimeout = time.Second * 30
)

// envVarSpec contains arguments specification for the env-vars extraction.
type envVarSpec struct {
	Port             int           `default:"8443"`                                                     // Webhook server port.
	TLSCertFile      string        `default:"/etc/tls-key-cert-pair/tls.crt" envconfig:"tls_cert_file"` // File containing the x509 Certificate for HTTPS.
	TLSKeyFile       string        `default:"/etc/tls-key-cert-pair/tls.key" envconfig:"tls_key_file"`  // File containing the x509 private key for TLSCERTFILE.
	ClusterName      string        `default:"cluster" split_words:"true"`                               // The name of the Kubernetes cluster.
	Timeout          time.Duration // server timeout. Defaults to the timeout passed by K8s API via query param. If not present, to the defaultTimeout const value.
	IgnoreNamespaces []string      `split_words:"true"` // The Webhook will ignore these namespaces.
}

func main() {
	var s envVarSpec
	s.IgnoreNamespaces = []string{
		metav1.NamespaceSystem,
		metav1.NamespacePublic,
	}
	err := envconfig.Process(envVarPrefix, &s)

	if err != nil {
		log.Fatal(err.Error())
	}

	logger := setupLogger()
	defer func() { _ = logger.Sync() }()

	pair, err := tls.LoadX509KeyPair(s.TLSCertFile, s.TLSKeyFile)
	if err != nil {
		logger.Errorw("failed to load key pair", "err", err)
	}

	watcher, _ := fsnotify.NewWatcher()
	defer func() { _ = watcher.Close() }()
	// Watch the parent directory of the key/cert files so we can catch
	// symlink updates of k8s secrets volumes and reload the certificates whenever they change.
	watchDir, _ := filepath.Split(s.TLSCertFile)
	if err := watcher.Add(watchDir); err != nil {
		logger.Errorw("could not watch folder", "folder", watchDir, "err", err)
	}

	k8sClient, err := k8s.New()
	if err != nil {
		logger.Fatalw("Couldn't connect to k8s api: %s", err)
	}

	whsvr := &server.Webhook{
		KeyFile:     s.TLSKeyFile,
		CertFile:    s.TLSCertFile,
		Cert:        &pair,
		ClusterName: s.ClusterName,
		CertWatcher: watcher,
		Server: &http.Server{
			Addr: fmt.Sprintf(":%d", s.Port),
		},
		Logger:           logger,
		IgnoreNamespaces: s.IgnoreNamespaces,
	}
	whsvr.Server.TLSConfig = &tls.Config{GetCertificate: whsvr.GetCert}
	whsvr.Mutators = append(whsvr.Mutators,
		server.NewEnvVarMutator(whsvr.ClusterName),
		server.NewSidecarMutator(whsvr.ClusterName, k8sClient))

	mux := http.NewServeMux()
	mux.Handle("/mutate", withLoggingMiddleware(logger)(withTimeoutMiddleware(s.Timeout)(whsvr)))
	whsvr.Server.Handler = mux

	// The health check needs to be in another server because it cannot be under TLS.
	readinessProbe := server.TLSReadyReadinessProbe(whsvr)
	go func() {
		logger.Info("starting the TLS readiness server")
		if err := http.ListenAndServe(":8080", readinessProbe); err != nil {
			logger.Errorw("failed to start TLS readiness server", "err", err)
		}
	}()

	go func() {
		logger.Info("starting the webhook server")
		if err := whsvr.Server.ListenAndServeTLS("", ""); err != nil {
			logger.Errorw("failed to start webhook server", "err", err)
		}
	}()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	var debounceTimer <-chan time.Time
	for {
		select {
		case <-debounceTimer:
			pair, err := tls.LoadX509KeyPair(whsvr.CertFile, whsvr.KeyFile)
			if err != nil {
				logger.Errorw("reload cert error", "err", err)
				break
			}
			whsvr.Lock()
			whsvr.Cert = &pair
			whsvr.Unlock()
			logger.Info("cert/key pair reloaded!")
		case event := <-whsvr.CertWatcher.Events:
			if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create {
				debounceTimer = time.After(500 * time.Millisecond)
			}
		case <-signalChan:
			logger.Info("got OS shutdown signal, shutting down webhook server gracefully...")
			_ = watcher.Close()
			_ = whsvr.Server.Shutdown(context.Background())
			return
		}
	}
}

func withTimeoutMiddleware(timeout time.Duration) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// In case the user does not set a timeout, we use the timeout passed by K8s API via query param.
			// If the latest timeout is not present in the form of URL query param, we use the defaultTimeout const value.
			if timeout.Nanoseconds() == 0 {
				if qt := r.URL.Query().Get("timeout"); qt != "" {
					timeout, _ = time.ParseDuration(qt)
				} else {
					timeout = defaultTimeout
				}
			}

			http.TimeoutHandler(next, timeout, "server timeout").ServeHTTP(w, r)
		})
	}
}

func withLoggingMiddleware(logger *zap.SugaredLogger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			logger.Infof("%s %s://%s%s %s\" from %s", r.Method, scheme, r.Host, r.RequestURI, r.Proto, r.RemoteAddr)

			next.ServeHTTP(w, r)
		}

		return http.HandlerFunc(fn)
	}
}

func setupLogger() *zap.SugaredLogger {
	config := zap.NewProductionConfig()
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder // We want human readable timestamps.

	zapLogger, err := config.Build()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	return zapLogger.Sugar()
}
