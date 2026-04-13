# swarmex-remediation

Self-healing with escalation chain for Docker Swarm.

## What it does

Monitors health failures and escalates remediation actions: restart → force-restart → drain node. Detects persistent failure patterns.

## Why it matters

Kubernetes restarts unhealthy pods automatically. Docker Swarm does too, but has no escalation — if a container keeps failing, Swarm just keeps restarting it forever. This controller adds intelligent escalation: after N failures, force-restart the task; after 2N, drain the entire node.

## Verified

- ✅ Detects container health_status and die/kill events
- ✅ Failure count with decay (resets after 5min of no failures)
- ✅ Escalation: restart (1x threshold) → force-restart (2x) → drain node (3x)

## Configuration

```yaml
deploy:
  labels:
    swarmex.remediation.enabled: "true"
    swarmex.remediation.failure-threshold: "5"
```
