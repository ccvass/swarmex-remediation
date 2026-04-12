package remediation

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
)

// EscalationLevel defines the remediation action severity.
type EscalationLevel int

const (
	LevelRestart EscalationLevel = iota
	LevelForceRestart
	LevelDrainNode
)

func (l EscalationLevel) String() string {
	return [...]string{"restart", "force-restart", "drain-node"}[l]
}

const (
	labelEnabled   = "swarmex.remediation.enabled"
	labelThreshold = "swarmex.remediation.failure-threshold"

	defaultThreshold = 5
	decayInterval    = 5 * time.Minute // failures decay after this period of no new failures
)

type failureRecord struct {
	count    int
	lastSeen time.Time
	nodeID   string
}

// Remediator monitors health failures and escalates remediation actions.
type Remediator struct {
	client   *client.Client
	logger   *slog.Logger
	failures map[string]*failureRecord // keyed by service ID
	mu       sync.Mutex
}

// New creates a Remediator.
func New(cli *client.Client, logger *slog.Logger) *Remediator {
	return &Remediator{
		client:   cli,
		logger:   logger,
		failures: make(map[string]*failureRecord),
	}
}

// HandleEvent processes Docker health and task state events.
func (r *Remediator) HandleEvent(ctx context.Context, event events.Message) {
	switch {
	case event.Type == events.ContainerEventType && event.Action == "health_status: unhealthy":
		serviceID := event.Actor.Attributes["com.docker.swarm.service.id"]
		nodeID := event.Actor.Attributes["com.docker.swarm.node.id"]
		if serviceID != "" {
			r.recordFailure(ctx, serviceID, nodeID)
		}
	case event.Type == "task" && (event.Action == "die" || event.Action == "kill"):
		serviceID := event.Actor.Attributes["com.docker.swarm.service.id"]
		nodeID := event.Actor.Attributes["com.docker.swarm.node.id"]
		if serviceID != "" {
			r.recordFailure(ctx, serviceID, nodeID)
		}
	}
}

func (r *Remediator) recordFailure(ctx context.Context, serviceID, nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.failures[serviceID]
	if !ok {
		rec = &failureRecord{}
		r.failures[serviceID] = rec
	}

	// Decay: reset if last failure was long ago
	if time.Since(rec.lastSeen) > decayInterval {
		rec.count = 0
	}

	rec.count++
	rec.lastSeen = time.Now()
	rec.nodeID = nodeID

	// Check if service has remediation enabled
	svc, _, err := r.client.ServiceInspectWithRaw(ctx, serviceID, types.ServiceInspectOptions{})
	if err != nil {
		return
	}
	if svc.Spec.Labels[labelEnabled] == "false" {
		return
	}

	threshold := defaultThreshold
	if t, ok := parseThreshold(svc.Spec.Labels[labelThreshold]); ok {
		threshold = t
	}

	if rec.count < threshold {
		r.logger.Warn("health failure recorded",
			"service", svc.Spec.Name, "count", rec.count, "threshold", threshold, "node", nodeID)
		return
	}

	level := r.determineLevel(rec.count, threshold)
	r.logger.Error("escalating remediation",
		"service", svc.Spec.Name, "level", level.String(), "failures", rec.count, "node", nodeID)

	switch level {
	case LevelRestart:
		r.forceUpdate(ctx, serviceID, svc)
	case LevelForceRestart:
		r.forceUpdate(ctx, serviceID, svc)
	case LevelDrainNode:
		r.drainNode(ctx, nodeID, svc.Spec.Name)
	}
}

func (r *Remediator) determineLevel(count, threshold int) EscalationLevel {
	switch {
	case count >= threshold*3:
		return LevelDrainNode
	case count >= threshold*2:
		return LevelForceRestart
	default:
		return LevelRestart
	}
}

func (r *Remediator) forceUpdate(ctx context.Context, serviceID string, svc swarm.Service) {
	svc.Spec.TaskTemplate.ForceUpdate++
	_, err := r.client.ServiceUpdate(ctx, serviceID, svc.Version, svc.Spec, types.ServiceUpdateOptions{})
	if err != nil {
		r.logger.Error("force update failed", "service", svc.Spec.Name, "error", err)
	}
}

func (r *Remediator) drainNode(ctx context.Context, nodeID, serviceName string) {
	if nodeID == "" {
		return
	}
	node, _, err := r.client.NodeInspectWithRaw(ctx, nodeID)
	if err != nil {
		r.logger.Error("node inspect failed", "node", nodeID, "error", err)
		return
	}

	node.Spec.Availability = swarm.NodeAvailabilityDrain
	err = r.client.NodeUpdate(ctx, nodeID, node.Version, node.Spec)
	if err != nil {
		r.logger.Error("node drain failed", "node", nodeID, "error", err)
		return
	}
	r.logger.Warn(fmt.Sprintf("node %s drained due to persistent failures in service %s", nodeID, serviceName))
}

// Cleanup removes stale failure records periodically.
func (r *Remediator) Cleanup(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.mu.Lock()
			for id, rec := range r.failures {
				if time.Since(rec.lastSeen) > decayInterval*2 {
					delete(r.failures, id)
				}
			}
			r.mu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}

func parseThreshold(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err == nil && n > 0
}
