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

	matchLatencySeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "bananatalk_match_latency_seconds",
		Help:    "Server-measured time between a user joining the queue and being matched with a peer.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
	})

	// connectTimeSeconds carries client-reported timings. The phase label
	// distinguishes the meaningful milestones in the WebRTC handshake, all
	// measured from the moment the client received the `match` message:
	//   total          — match → first remote frame rendered
	//   first_track    — match → onTrack fired (remote track exposed to app)
	//   ice_gathering  — match → iceGatheringState == complete
	//   sdp_offer      — match → local offer sent (offerer only)
	//   sdp_answer     — match → remote answer applied (offerer only)
	connectTimeSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "bananatalk_connect_time_seconds",
		Help:    "Client-reported time from match assignment to a connection milestone, by phase and role.",
		Buckets: []float64{0.1, 0.25, 0.5, 0.75, 1, 1.5, 2, 3, 4, 6, 10, 20},
	}, []string{"phase", "role"})

	queueWaitSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "bananatalk_queue_wait_seconds",
		Help:    "Client-reported time the user spent in the matching queue before a match.",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300},
	})
)

func init() {
	prometheus.MustRegister(
		activeConnections,
		matchesTotal,
		reportsTotal,
		matchLatencySeconds,
		connectTimeSeconds,
		queueWaitSeconds,
	)
}

func metricsHandler() http.Handler {
	return promhttp.Handler()
}
