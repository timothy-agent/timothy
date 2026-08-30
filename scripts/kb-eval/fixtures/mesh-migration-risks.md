# Zero Downtime Istio Migration Risks

Migrating an existing cluster onto Istio without downtime carries a
specific set of risks that teams underestimate. The sidecar injection
step alone can break workloads that assume a single container per
pod, since liveness and readiness probes on the original container
may fail while the sidecar is still starting.

## Sidecar rollout order

Rolling out sidecars namespace by namespace, rather than cluster wide,
limits the blast radius of a bad injection. Start with a low traffic
namespace, watch error rates for a full business cycle, then proceed.
Skipping this staged rollout is the single most common cause of a
failed migration.

## mTLS mode transitions

Istio's permissive mode allows plaintext and mTLS traffic
simultaneously during migration, which is safer than jumping straight
to strict mode. Flipping to strict mode before every workload in a
namespace has a sidecar causes silent connection failures that are
hard to diagnose because the error looks like a generic timeout
rather than a TLS handshake rejection.

## Resource overhead

Each sidecar proxy consumes CPU and memory that the original capacity
planning did not account for. Small clusters running near their node
limits can see pod scheduling failures once sidecars are injected
cluster wide. Budget roughly 100 to 250 megabytes of memory and a
tenth of a CPU core per sidecar as a starting estimate, then adjust
from real usage.

## Rollback plan

Every migration wave needs a tested rollback: removing the sidecar
injection label from a namespace and restarting its pods. Teams that
skip rehearsing this step often discover during an actual incident
that rollback takes far longer than expected because of unexpected
interactions with existing network policies.

## Observability gaps

Istio's own telemetry can mask upstream failures if dashboards are
not updated to distinguish sidecar level metrics from application
level metrics. Confirm before migrating that on call engineers can
tell the difference between a proxy failure and an application
failure from the existing dashboards.
