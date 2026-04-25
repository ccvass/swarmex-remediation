<p align="center"><img src="https://raw.githubusercontent.com/ccvass/swarmex/main/docs/assets/logo.svg" alt="Swarmex" width="400"></p>

[![Test, Build & Deploy](https://github.com/ccvass/swarmex-remediation/actions/workflows/publish.yml/badge.svg)](https://github.com/ccvass/swarmex-remediation/actions/workflows/publish.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

# Swarmex Remediation

Self-healing with escalation chain and disruption budgets for Docker Swarm services.

Part of [Swarmex](https://github.com/ccvass/swarmex) — enterprise-grade orchestration for Docker Swarm.

## What It Does

Monitors service health and automatically remediates failures through a three-level escalation chain: restart → force-update → drain node. Disruption budgets ensure that remediation actions never take down more instances than allowed.

**v1.1.0**: Tracks failures per service+node pair. Only drains a node when 3+ distinct services fail on it (node-level issue). Single-service failures cap at force-restart — never drain.

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

1. Records health check failures per service+node pair and tracks failure counts.
2. On reaching the failure threshold, escalates: first restarts the task.
3. If failures persist, performs a force-update on the service.
4. Drain only triggers when 3+ distinct services fail on the same node (indicates node-level problem, not app bug).
5. Never drains the last active manager.
6. Checks disruption budgets before force-restart and drain to ensure minimum availability.
7. Failure counts decay after 5 minutes of no new failures.

## Quick Start

```bash
docker service update \
  --label-add swarmex.remediation.enabled=true \
  --label-add swarmex.remediation.failure-threshold=3 \
  --label-add swarmex.disruption.min-available=2 \
  my-app
```

## Verified

- Drain correctly blocked for single-service failures (downgraded to force-restart)
- Drain triggers when 3+ services fail on same node
- Drain blocked when it would violate the min-available disruption budget
- Never drains the last active manager

## License

Apache-2.0
