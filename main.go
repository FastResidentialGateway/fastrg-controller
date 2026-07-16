package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	_ "fastrg-controller/docs"
	"fastrg-controller/internal/db"
	"fastrg-controller/internal/kafka"
	"fastrg-controller/internal/leader"
	"fastrg-controller/internal/projection"
	"fastrg-controller/internal/server"
	"fastrg-controller/internal/storage"

	"gopkg.in/natefinch/lumberjack.v2"
)

// @title           FastRG Controller API
// @version         1.0
// @description     FastRG Controller REST API server for managing nodes and HSI configurations.
// @termsOfService  http://swagger.io/terms/

// @contact.name   API Support
// @contact.url    https://github.com/FastResidentialGateway/fastrg-controller
// @contact.email  w180112@gmail.com

// @license.name  BSD-3
// @license.url   https://opensource.org/licenses/BSD-3-Clause

// @BasePath  /api

// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description JWT token for authentication. Enter your token directly (without Bearer prefix).

func main() {
	// Get ports from environment variables with defaults
	grpcPort := os.Getenv("GRPC_PORT")
	if grpcPort == "" {
		grpcPort = "50051"
	}

	httpPort := os.Getenv("HTTP_REDIRECT_PORT")
	if httpPort == "" {
		httpPort = "8080"
	}

	httpsPort := os.Getenv("HTTPS_PORT")
	if httpsPort == "" {
		httpsPort = "8443"
	}

	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: time.StampMicro,
		CallerPrettyfier: func(frame *runtime.Frame) (function string, file string) {
			fileName := path.Base(frame.File)
			return "", fileName + ":" + strconv.Itoa(frame.Line)
		},
	})
	// Redirect logrus output to a file under /var/log/fastrg-controller
	logDir := "/var/log/fastrg-controller"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		logrus.WithError(err).Fatalf("Failed to create log directory: %s", logDir)
	}
	fileLogger := &lumberjack.Logger{
		Filename:   path.Join(logDir, "controller.log"),
		MaxSize:    100, // megabytes
		MaxBackups: 3,
		MaxAge:     28,   // days
		Compress:   true, // disabled by default
	}
	defer fileLogger.Close()
	var output io.Writer
	output = fileLogger
	output = io.MultiWriter(output, os.Stderr)
	logrus.SetOutput(output)
	logrus.SetReportCaller(true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := startLogServer(logDir)
	defer func() {
		srv.Shutdown(ctx)
	}()

	// connect to etcd
	etcd, err := storage.NewEtcdClient()
	if err != nil {
		logrus.WithError(err).Fatal("failed to connect etcd")
	}
	defer etcd.Close()

	jwtSecret, err := server.ResolveJWTSecret(ctx, etcd)
	if err != nil {
		logrus.WithError(err).Fatal("failed to resolve JWT secret")
	}

	// Start Prometheus metrics server
	if err := server.StartPrometheusServer(); err != nil {
		logrus.WithError(err).Error("failed to start Prometheus metrics server")
	}

	// Create shared NodeMonitorManager (used by both gRPC and REST servers)
	// Pass database for stateless recovery of PPPoE status
	nmm := server.NewNodeMonitorManager(nil)
	rest := server.NewRestServer(etcd, nmm, nil, jwtSecret)

	// Optional PostgreSQL read model. Connection attempts run in the background
	// so every listener can start in etcd-only mode even when PostgreSQL is late.
	// ConnectLoop publishes a database exactly once after migrations succeed.
	if dsn := db.DSN(); dsn != "" {
		go db.ConnectLoop(ctx, dsn, func(database *db.DB) {
			nmm.SetDatabase(database)
			rest.SetDatabase(database)

			// The Kafka consumer runs on every replica: a single consumer group
			// (KAFKA_GROUP) balances partitions across them, so this is safe and
			// HA without leader election.
			if brokers := kafka.Brokers(); brokers != nil {
				go kafka.NewConsumer(brokers, database, etcd).Run(ctx)
			} else {
				logrus.Info("KAFKA_BROKERS not set; running without Kafka consumer")
			}
		})
	} else {
		logrus.Info("DATABASE_URL/POSTGRES_HOST not set; running without PostgreSQL projection")
	}

	// Leader election (etcd-based): every replica serves REST/gRPC and runs the
	// Kafka consumer, but only the leader runs the singleton background workers —
	// the etcd->PostgreSQL projection (single writer of the config tables),
	// stale-node eviction, and per-node stats scraping — so they are not
	// duplicated across replicas. A single instance wins immediately.
	go leader.Run(ctx, etcd.Client(), "fastrg-controller/leader", func(leaderCtx context.Context) {
		logrus.Infof("Became leader (%s): starting node-state workers", leader.Identity())
		nmm.SetLeader(true)
		go startProjectionWhenDatabaseReady(leaderCtx, etcd, nmm)
		go func() {
			<-leaderCtx.Done()
			nmm.SetLeader(false)
			logrus.Info("Lost leadership: stopped projection + node-state workers")
		}()
	})

	// CLI-facing config gRPC service (shares same port as NodeManagement)
	configSvc := server.NewConfigGrpcServer(etcd, jwtSecret)

	var wg sync.WaitGroup

	// start gRPC server
	wg.Go(func() {
		grpcSrv := server.NewGrpcServer(etcd, nmm)
		logrus.Infof("Starting gRPC server on :%s", grpcPort)
		grpcSrv.Start(":"+grpcPort, configSvc)
	})

	// start HTTP redirect servers
	logrus.Infof("Starting HTTP redirect server on :%s", httpPort)
	if httpSrv, err := server.StartHTTPRedirectServer(":" + httpPort); err != nil {
		logrus.WithError(err).Error("failed to start HTTP redirect server")
	} else {
		defer func() {
			httpSrv.Shutdown(ctx)
		}()
	}

	// start REST API (HTTPS)
	logrus.Infof("Starting HTTPS server on :%s", httpsPort)
	if err := rest.StartRestServer(":" + httpsPort); err != nil {
		logrus.WithError(err).Fatal("failed to start HTTPS server")
	}
}

// startProjectionWhenDatabaseReady covers both event orders: an already-ready
// database starts immediately after election, while a leader elected first
// waits until ConnectLoop publishes the database. The wait and projection are
// both bound to leaderCtx, so losing leadership stops either phase.
func startProjectionWhenDatabaseReady(leaderCtx context.Context, etcd *storage.EtcdClient, nmm *server.NodeMonitorManager) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		if database := nmm.Database(); database != nil {
			logrus.Info("Started config projection (etcd -> PostgreSQL)")
			projection.New(etcd, database).Run(leaderCtx)
			return
		}

		select {
		case <-leaderCtx.Done():
			return
		case <-ticker.C:
		}
	}
}

func startLogServer(logDir string) *http.Server {
	// start HTTPS server to expose log file on :8444 (or $LOG_HTTPS_PORT)
	logHTTPSPort := os.Getenv("LOG_HTTPS_PORT")
	if logHTTPSPort == "" {
		logHTTPSPort = "8444"
	}
	logFilePath := filepath.Join(logDir, "controller.log")

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		f, err := os.Open(logFilePath)
		if err != nil {
			http.Error(w, "log file not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			http.Error(w, "failed to stat log file", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.ServeContent(w, r, filepath.Base(logFilePath), fi.ModTime(), f)
	})

	certFile := os.Getenv("CERT_FILE")
	if certFile == "" {
		certFile = "./certs/server.crt"
	}
	keyFile := os.Getenv("KEY_FILE")
	if keyFile == "" {
		keyFile = "./certs/server.key"
	}

	srv := server.NewHardenedTLSServer(":"+logHTTPSPort, nil)
	go func() {
		logrus.Infof("Starting log HTTPS server on :%s", logHTTPSPort)
		if err := srv.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
			logrus.WithError(err).Error("log HTTPS server failed")
		}
	}()

	return srv
}
