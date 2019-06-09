package raft

import "github.com/prometheus/client_golang/prometheus"

// metricHolder holds metrics from the nodes perspective.
//
// Aim to track;
// - errors
// - utilisation
// - saturation
//
// http://www.brendangregg.com/usemethod.html
//
// Centralising the metrics: the key advantage of having the metrics for the package in one place is that it becomes
// easier to present a consistent set of metrics. Consistent metrics make for better operations and debugging.
//
type metricsHolder struct {
	registry *prometheus.Registry
	// Are we tracking expensive metrics?
	detailed bool
}

// Set up a metricsHolder to collect metrics for a given node.
func initMetrics(registry *prometheus.Registry, detailed bool) *metricsHolder {

	if registry == nil {
		var ok bool
		registry, ok = prometheus.DefaultRegisterer.(*prometheus.Registry)
		if !ok {
			return nil
		}
	}

	mh := &metricsHolder{
		detailed: detailed,
		registry: registry,
	}

	return mh
}
