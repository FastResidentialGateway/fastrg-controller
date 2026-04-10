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
	"fastrg-controller/internal/server"
	"fastrg-controller/internal/storage"

	"gopkg.in/natefinch/lumberjack.v2"
)

// @title           FastRG Controller API
// @version         1.0
// @description     FastRG Controller REST API server for managing nodes and HSI configurations.
// @termsOfService  http://swagger.io/terms/

// @contact.name   API Support
// @contact.url    https://github.com/w180112/fastrg-controller
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

	// Start failed events watcher
	cancelWatcher := storage.StartFailedEventsWatcher(etcd)
	defer cancelWatcher()

	// Start Prometheus metrics server
	if err := server.StartPrometheusServer(); err != nil {
		logrus.WithError(err).Error("failed to start Prometheus metrics server")
	}

	// Create shared NodeMonitorManager (used by both gRPC and REST servers)
	nmm := server.NewNodeMonitorManager()

	var wg sync.WaitGroup

	// start gRPC server
	wg.Go(func() {
		grpcSrv := server.NewGrpcServer(etcd, nmm)
		logrus.Infof("Starting gRPC server on :%s", grpcPort)
		grpcSrv.Start(":" + grpcPort)
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
	rest := server.NewRestServer(etcd, nmm)
	logrus.Infof("Starting HTTPS server on :%s", httpsPort)
	if err := rest.StartRestServer(":" + httpsPort); err != nil {
		logrus.WithError(err).Fatal("failed to start HTTPS server")
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

	srv := &http.Server{Addr: ":" + logHTTPSPort}
	go func() {
		logrus.Infof("Starting log HTTPS server on :%s", logHTTPSPort)
		if err := srv.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
			logrus.WithError(err).Error("log HTTPS server failed")
		}
	}()

	return srv
}
