package server

import (
	"fmt"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

func newMetricsMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	return mux
}

// StartPrometheusServer starts the Prometheus metrics HTTP server
func StartPrometheusServer() error {
	// Get Prometheus listen IP from environment variable
	// Default to 127.0.0.1 if not set
	prometheusIP := os.Getenv("PROMETHEUS_LISTEN_IP")
	if prometheusIP == "" {
		prometheusIP = "127.0.0.1"
		logrus.Infof("PROMETHEUS_LISTEN_IP not set, using default: %s", prometheusIP)
	}

	// Prometheus metrics port is fixed at 55688
	prometheusPort := "55688"
	addr := fmt.Sprintf("%s:%s", prometheusIP, prometheusPort)

	// Keep metrics routes isolated from the process-wide default mux and from
	// the log HTTPS listener.
	mux := newMetricsMux()

	logrus.Infof("Starting Prometheus metrics server on %s", addr)

	// Start server in a goroutine
	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			logrus.WithError(err).Error("Prometheus metrics server error")
		}
	}()

	return nil
}
