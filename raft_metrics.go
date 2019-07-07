package raft

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
)

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
	//
	// Metrics
	// A slightly unorthodox use of metrics in some cases; we export state as metrics too (e.g. current role
	// of node, which node the current node thinks is the leader etc).
	stateGauge prometheus.Gauge
	leader     prometheus.Gauge
}

// Set up a metricsHolder to collect metrics for a given node.
func initMetrics(registry *prometheus.Registry, namespace string, detailed bool, nodeIndex int32) *metricsHolder {

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

	// We include a const label to indicate which node index in the cluster is originating the metric. In production
	// environments the node could typically be inferred from labels added externally as part of the deployment (e.g.
	// kubernetes prometheus operator jobLabel). Incorporating a label tied to the config provides an unambiguous,
	// possibly redundant target label in the metrics.

	mh.stateGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   namespace,
		Subsystem:   "raft",
		Name:        "role",
		Help:        "role indicates which state node is in at sampling time: follower, candidate or leader (1,2,3 respectively).",
		ConstLabels: map[string]string{"nodeIndex": fmt.Sprint(nodeIndex)},
	})

	mh.leader = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   namespace,
		Subsystem:   "raft",
		Name:        "leader",
		Help:        "current leader from the perspective of this node (-1 indicate no leader).",
		ConstLabels: map[string]string{"nodeIndex": fmt.Sprint(nodeIndex)},
	})

	registry.MustRegister(
		mh.stateGauge,
		mh.leader)

	return mh
}
