package remediation

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
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
	labelEnabled       = "swarmex.remediation.enabled"
	labelThreshold     = "swarmex.remediation.failure-threshold"
	labelMinAvailable  = "swarmex.disruption.min-available"
	labelMaxUnavail    = "swarmex.disruption.max-unavailable"

	defaultThreshold = 5
	decayInterval    = 5 * time.Minute // failures decay after this period of no new failures
)

const (
	// minServicesForDrain is the minimum number of distinct services that must
	// fail on the same node before a drain is considered. This prevents a
	// single misbehaving app from taking down an entire node.
	minServicesForDrain = 3
)

type failureRecord struct {
	count    int
	lastSeen time.Time
}

// Remediator monitors health failures and escalates remediation actions.
type Remediator struct {
	client   *client.Client
	logger   *slog.Logger
	failures map[string]*failureRecord // keyed by "serviceID:nodeID"
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
	if event.Type != events.ContainerEventType {
		return
	}
	action := string(event.Action)
	if action != "die" && action != "kill" && action != "health_status: unhealthy" {
		return
	}
	serviceID := event.Actor.Attributes["com.docker.swarm.service.id"]
	nodeID := event.Actor.Attributes["com.docker.swarm.node.id"]
	if serviceID != "" {
		r.recordFailure(ctx, serviceID, nodeID)
	}
}

func (r *Remediator) recordFailure(ctx context.Context, serviceID, nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := serviceID + ":" + nodeID
	rec, ok := r.failures[key]
	if !ok {
		rec = &failureRecord{}
		r.failures[key] = rec
	}

	// Decay: reset if last failure was long ago
	if time.Since(rec.lastSeen) > decayInterval {
		rec.count = 0
	}

	rec.count++
	rec.lastSeen = time.Now()

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

	// Never drain for a single service — only restart/force-restart
	if level == LevelDrainNode {
		if !r.isNodeLevelFailure(nodeID) {
			r.logger.Warn("drain downgraded to force-restart — only one service failing on node",
				"service", svc.Spec.Name, "node", nodeID, "failures", rec.count)
			level = LevelForceRestart
		}
	}

	r.logger.Error("escalating remediation",
		"service", svc.Spec.Name, "level", level.String(), "failures", rec.count, "node", nodeID)

	switch level {
	case LevelRestart:
		r.forceUpdate(ctx, serviceID, svc)
	case LevelForceRestart:
		if r.canDisruptService(ctx, svc) {
			r.forceUpdate(ctx, serviceID, svc)
		} else {
			r.logger.Warn("force-restart blocked by disruption budget", "service", svc.Spec.Name)
		}
	case LevelDrainNode:
		r.drainNode(ctx, nodeID, svc.Spec.Name)
	}
}

// isNodeLevelFailure checks if multiple distinct services are failing on the
// same node, which indicates a node-level problem rather than an app bug.
func (r *Remediator) isNodeLevelFailure(nodeID string) bool {
	suffix := ":" + nodeID
	distinctServices := 0
	for key, rec := range r.failures {
		if len(key) > len(suffix) && key[len(key)-len(suffix):] == suffix && rec.count >= defaultThreshold {
			distinctServices++
		}
	}
	return distinctServices >= minServicesForDrain
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

	// Check disruption budgets for all services on this node
	if !r.canDisruptNode(ctx, nodeID) {
		r.logger.Warn("drain blocked by disruption budget", "node", nodeID, "service", serviceName)
		return
	}

	// Safety: never drain the last active manager
	nodes, err := r.client.NodeList(ctx, types.NodeListOptions{})
	if err != nil {
		return
	}
	activeManagers := 0
	for _, n := range nodes {
		if n.Spec.Role == swarm.NodeRoleManager && n.Spec.Availability == swarm.NodeAvailabilityActive {
			activeManagers++
		}
	}

	node, _, err := r.client.NodeInspectWithRaw(ctx, nodeID)
	if err != nil {
		r.logger.Error("node inspect failed", "node", nodeID, "error", err)
		return
	}

	if node.Spec.Role == swarm.NodeRoleManager && activeManagers <= 1 {
		r.logger.Warn("skipping drain — this is the last active manager", "node", nodeID, "service", serviceName)
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

// canDisruptNode checks disruption budgets for all services with tasks on this node.
func (r *Remediator) canDisruptNode(ctx context.Context, nodeID string) bool {
	services, err := r.client.ServiceList(ctx, types.ServiceListOptions{})
	if err != nil {
		return true // fail open
	}
	for _, svc := range services {
		minAvailStr := svc.Spec.Labels[labelMinAvailable]
		if minAvailStr == "" {
			continue
		}
		minAvail, ok := parseThreshold(minAvailStr)
		if !ok {
			continue
		}
		// Count running tasks NOT on this node
		tasks, err := r.client.TaskList(ctx, types.TaskListOptions{
			Filters: filters.NewArgs(filters.Arg("service", svc.ID), filters.Arg("desired-state", "running")),
		})
		if err != nil {
			continue
		}
		surviving := 0
		for _, t := range tasks {
			if t.NodeID != nodeID && t.Status.State == swarm.TaskStateRunning {
				surviving++
			}
		}
		if surviving < minAvail {
			r.logger.Warn("disruption budget violated",
				"service", svc.Spec.Name, "min_available", minAvail, "surviving", surviving)
			return false
		}
	}
	return true
}

// canDisruptService checks if force-updating a service respects max-unavailable.
func (r *Remediator) canDisruptService(ctx context.Context, svc swarm.Service) bool {
	maxUnavailStr := svc.Spec.Labels[labelMaxUnavail]
	if maxUnavailStr == "" {
		return true
	}
	maxUnavail, ok := parseThreshold(maxUnavailStr)
	if !ok {
		return true
	}
	tasks, err := r.client.TaskList(ctx, types.TaskListOptions{
		Filters: filters.NewArgs(filters.Arg("service", svc.ID), filters.Arg("desired-state", "running")),
	})
	if err != nil {
		return true
	}
	running := 0
	for _, t := range tasks {
		if t.Status.State == swarm.TaskStateRunning {
			running++
		}
	}
	desired := 1
	if svc.Spec.Mode.Replicated != nil && svc.Spec.Mode.Replicated.Replicas != nil {
		desired = int(*svc.Spec.Mode.Replicated.Replicas)
	}
	unavailable := desired - running
	return unavailable < maxUnavail
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
