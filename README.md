<p align="center"><img src="https://raw.githubusercontent.com/ccvass/swarmex/main/docs/assets/logo.svg" alt="Swarmex" width="400"></p>

# Swarmex Remediation

Self-healing with escalation chain and disruption budgets for Docker Swarm services.

Part of [Swarmex](https://github.com/ccvass/swarmex) — enterprise-grade orchestration for Docker Swarm.

## What It Does

Monitors service health and automatically remediates failures through a three-level escalation chain: restart → force-update → drain node. Disruption budgets ensure that remediation actions never take down more instances than allowed.

## Labels

```yaml
deploy:
  labels:
    swarmex.remediation.enabled: "true"          # Enable self-healing
    swarmex.remediation.failure-threshold: "3"   # Failures before escalation
    swarmex.disruption.min-available: "2"        # Min healthy instances required
    swarmex.disruption.max-unavailable: "1"      # Max instances down at once
```

## How It Works

1. Records health check failures per service and tracks failure counts.
2. On reaching the failure threshold, escalates: first restarts the task.
3. If failures persist, performs a force-update on the service.
4. As a last resort, drains the problematic node (never the last manager).
5. Checks disruption budgets before force-restart and drain to ensure minimum availability.

## Quick Start

```bash
docker service update \
  --label-add swarmex.remediation.enabled=true \
  --label-add swarmex.remediation.failure-threshold=3 \
  --label-add swarmex.disruption.min-available=2 \
  my-app
```

## Verified

Node drained on persistent failures. Drain correctly blocked when it would violate the min-available disruption budget.

## License

Apache-2.0
