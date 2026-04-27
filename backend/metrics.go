package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	activeConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "bananatalk_active_connections",
		Help: "Number of currently authenticated WebSocket clients.",
	})

	matchesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "bananatalk_matches_total",
		Help: "Total number of successful pair matches made by the matchmaker.",
	})

	reportsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bananatalk_reports_total",
		Help: "Total number of accepted user reports, labelled by auto-ban outcome.",
	}, []string{"banned"})
)

func init() {
	prometheus.MustRegister(activeConnections, matchesTotal, reportsTotal)
}

func metricsHandler() http.Handler {
	return promhttp.Handler()
}
