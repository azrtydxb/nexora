import type { HelpArea, HelpEntry } from "./types";

// Shared by the "New engine group" dialog and the group settings form, which use different ids.
const groupName: HelpEntry = {
  text: "A unique name for the engine group, used in join tokens, the CLI and the Engine group field of scoped configuration.",
  range: "Lowercase letters, digits and dashes, at most 63 characters",
  topic: "fleet",
  anchor: "engine-groups",
};
const groupDescription: HelpEntry = {
  text: "A free-text note for operators, for example where the group's engines run. It does not change resolution.",
  range: "At most 1,024 characters",
};
const upstreamMode: HelpEntry = {
  text: "How upstreams scoped to this group combine with the global ones. Inherit tries the group's upstreams first, then the global ones; override uses only the group's.",
  default: "Inherit",
  topic: "fleet",
  anchor: "engine-groups",
};
const rolloutStrategy: HelpEntry = {
  text: "How a new configuration version reaches this group. all_at_once sends it to every engine together; canary sends it to a few engines first, watches their health, and only then to the rest.",
  default: "all_at_once",
  effect:
    "Rollbacks and resumed rollouts always go to the whole group at once.",
  topic: "fleet",
  anchor: "rollouts",
};
const canaryCount: HelpEntry = {
  text: "How many connected engines receive a canary rollout first. Nexora uses the larger of this and the percentage, picks engines labelled nexora.io/canary=true first, then by node name, and always leaves at least one engine out when two or more are connected.",
  default: "1 for a new group",
  range: "0 or more; the count or the percentage must be above 0",
  topic: "fleet",
  anchor: "rollouts",
};
const canaryPercent: HelpEntry = {
  text: "The share of connected engines that receive a canary rollout first. Nexora uses the larger of this and the canary engine count.",
  default: "0",
  range: "0–100",
  topic: "fleet",
  anchor: "rollouts",
};

export const fleetHelp: HelpArea = {
  pages: [
    "pages/EnginesPage.tsx",
    "pages/EngineGroupPage.tsx",
    "pages/EngineDetailPage.tsx",
    "pages/RolloutPage.tsx",
    "components/fleet.tsx",
    "components/EngineModal.tsx",
  ],
  entries: {
    "engines-col-status": {
      text: "current: runs its target version. behind: connected, not there yet. rejected: refused a newer version, the reason is under Problem. disconnected: no live control connection. ahead: runs a newer version than its target, for example after a database restore. revoked: its certificates are revoked; it keeps serving its last configuration.",
      topic: "fleet",
    },
    "enginegroup-name": { ...groupName },
    "enginegroup-settings-name": {
      ...groupName,
      effect: "The default group cannot be renamed.",
    },
    "enginegroup-new-description": { ...groupDescription },
    "enginegroup-description": { ...groupDescription },
    "enginegroup-upstream-mode": { ...upstreamMode },
    "enginegroup-settings-upstreams": { ...upstreamMode },
    "enginegroup-strategy": { ...rolloutStrategy },
    "enginegroup-settings-strategy": { ...rolloutStrategy },
    "enginegroup-canary-count": { ...canaryCount },
    "enginegroup-settings-canary-count": { ...canaryCount },
    "enginegroup-canary-percent": { ...canaryPercent },
    "enginegroup-settings-canary-percent": { ...canaryPercent },
    "enginegroup-acl": {
      text: "Client networks allowed to use this group's engines as a resolver in addition to the global recursion access list, one prefix per line.",
      default: "Empty (only the global list)",
      range: "At most 256 prefixes",
      effect: "Adds to the global list; it cannot remove networks from it.",
      topic: "access-control",
      anchor: "recursion-access",
    },
    "enginegroup-otlp": {
      text: "The OpenTelemetry collector this group's engines push metrics, traces and, with the OpenSearch query log, query log records to. An unreachable endpoint drops that data but never slows queries.",
      default: "Empty (the endpoint in Settings)",
      range: "At most 512 characters",
      topic: "observability",
      anchor: "traces",
    },
    "enginegroup-ack-timeout": {
      text: "How long engines have to apply a new version, first the canaries and then the rest of the group. A connected engine that has not applied it in time halts the rollout; disconnected engines get it when they reconnect.",
      default: "60 seconds",
      range: "5–3600 seconds",
      topic: "fleet",
      anchor: "rollouts",
    },
    "enginegroup-health-window": {
      text: "How long canary engines must run a new configuration before the rollout continues. Each canary needs at least two stats samples in the window, and a SERVFAIL ratio above the limit halts the rollout.",
      default: "30 seconds",
      topic: "fleet",
      anchor: "rollouts",
    },
    "enginegroup-max-servfail": {
      text: "The highest share of SERVFAIL answers the canaries may give during the health window. Above it, the rollout halts.",
      default: "5 %",
      range: "0–100 %",
      effect:
        "Only applies once the canaries answered at least the minimum number of queries.",
      topic: "fleet",
      anchor: "rollouts",
    },
    "enginegroup-min-queries": {
      text: "How many queries the canaries must answer in the health window before the SERVFAIL limit is judged. With fewer, the SERVFAIL limit is not checked.",
      default: "100",
      range: "0 or more",
      topic: "fleet",
      anchor: "rollouts",
    },
    "rollback-version": {
      text: "The earlier version to return to. Its configuration is republished as a new version to every engine in the group at once.",
      effect:
        "Rollouts of later changes pause for this group until you resume them, because the configuration still contains the rolled-back change.",
      topic: "fleet",
      anchor: "rollouts",
    },
    "jointoken-name": {
      text: "A label for the token in this list, for example the rack or cluster its engines run in. The token itself is shown once after creation.",
      range: "At most 64 characters",
      topic: "fleet",
      anchor: "engine-groups",
    },
    "jointoken-ttl": {
      text: "How long the token can enroll new engines. Enrolled engines never need it again, so expiry does not affect them.",
      default: "24 hours",
      topic: "fleet",
      anchor: "engine-groups",
    },
    "jointoken-engine-group": {
      text: "The engine group engines enrolled with this token join. Move an engine to another group later on its engine page.",
      topic: "fleet",
      anchor: "engine-groups",
    },
    "jointoken-max-uses": {
      text: "How many engines may enroll with this token. Leave it empty for a DaemonSet or an autoscaled Deployment, where every pod enrolls with the same token.",
      default: "Empty (unlimited until it expires)",
      range: "1–100,000",
      topic: "fleet",
      anchor: "engine-groups",
    },
    "jointoken-labels": {
      text: "Labels copied to every engine enrolled with this token. The label nexora.io/canary=true makes an engine a preferred canary.",
      range: "At most 32 labels",
      topic: "fleet",
      anchor: "rollouts",
    },
    "engine-group-select": {
      text: "The engine group this engine belongs to. Saving moves it and hands it that group's configuration at once.",
      effect:
        "The target group's stable configuration is republished as a new version.",
      topic: "fleet",
      anchor: "engine-groups",
    },
    "engine-labels": {
      text: "Key and value pairs describing this engine; rows with an empty key are dropped. The label nexora.io/canary=true makes it a preferred canary in its group.",
      range: "At most 32 labels",
      topic: "fleet",
      anchor: "rollouts",
    },
    "rollout-state": {
      text: "pending: waiting for canaries to be picked, or held while rollouts are paused. canary: canaries are applying. verifying: the health window is running. rolling: the rest of the group is applying. completed, halted, rolled_back, or superseded by a newer change.",
      effect:
        "When a rollout halts, the canaries keep the new version and the other engines stay on the stable one.",
      topic: "fleet",
      anchor: "rollouts",
    },
    "rollout-col-progress": {
      text: "applied: the engine runs this version. waiting: it has not confirmed it yet. rejected: it refused the version, the reason is under Problem. disconnected: it gets the version when it reconnects.",
      topic: "fleet",
      anchor: "rollouts",
    },
    "engine-logs-level": {
      text: "The lowest level to show, from the engine's own log: error, warn, info or debug. Debug shows every line. The engine keeps only its last 2,000 lines in memory; older lines are dropped and lines are lost when it restarts.",
      default: "debug",
      topic: "fleet",
      anchor: "engine-logs",
    },
  },
};
