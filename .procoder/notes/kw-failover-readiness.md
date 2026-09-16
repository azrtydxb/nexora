# kw failover readiness readers

Status: standalone readers implemented and tested; guarded deployment integration unfinished. Production remains two engines on `sha-55dcf46`. No production resources were changed in this work.

## Implemented

- `deploy/kwrollout/workload.go`: explicit-context controller/pod inspection; exact immutable default-group selector, controller UID ownership, expected node, one available/Ready member, running engine container, preserved hostPath/mount, image checks for updated members, and stable engine node-name expansion.
- Read only `/var/lib/nexora/identity/engine_id` (public UUID) through bounded `kubectl exec`; no private key, certificate or token read. Re-read the pod and reject replacement, restart or other changes across identity observation. Frozen partners may retain their previous image while their desired template changes.
- `deploy/kwrollout/endpoints.go`: validate the intended legacy or paired Service selector, original VIP, Local source-IP policy, and all five kw transport mappings. Compare actual EndpointSlices against verified pod UIDs, names, addresses and nodes; require Service owner UID, Ready/nonterminating endpoints and complete per-transport membership. Reject cross-pair, duplicate, stale, missing and colocated members. Re-read Service to reject observation races.
- `kubectl.go` shares the existing bounded explicit-context transport with the lock adapter. It does not print credential-plugin stderr.

## Failure and correction

The initial live workload test failed with `pod changed while reading engine identity` (`/tmp/nexora-workload-live.log`). Read-only comparison showed semantically identical objects: a nested `json.RawMessage` retained different indentation in list versus single-object kubectl output. `TestReadServingPodIgnoresJSONIndentation` reproduced the failure (`/tmp/nexora-workload-json-red.log`). Parsing the running-state fields into a typed structure fixed representation-only differences without ignoring UID, resourceVersion, container identity, restart count or readiness changes.

## Evidence

- Final Linux `go test -race ./deploy/kwrollout -count=1`: passed, 2.178s.
- Final Linux `go test ./deploy/deploytest -count=1`: passed, 4.415s.
- Output: `/tmp/nexora-topology-linux.log`.
- Read-only live check: `NEXORA_KW_WORKLOAD_TEST=1 NEXORA_KW_EXPECT_IMAGE=192.168.10.131/azrtydxb/nexora-engine:sha-55dcf46 go test ./deploy/kwrollout -run '^TestReadServingPodLive$' -count=1 -v`: passed, test 1.19s/package 2.531s. Verified a/master-12 and b/master-13, distinct public UUIDs, preserved state, expected image and their existing VIP endpoints. Output: `/tmp/nexora-topology-live.log`.
- Full laptop `procoder test` remains red: 230 Go failures and Rust `mmsghdr` compilation failure. Reader tests do not supersede prior failed strict release acceptance.

## Still required

Assemble fleet-wide discovery/classification and cross-pair state/identity isolation checks; verify desired node affinity and kube-vip eligibility/capacity; construct the authenticated management client and configured DNS probe expectation; implement UID/resourceVersion-safe label enrolment, Helm stages and partial-migration recovery. Hold the deployment lock and monitor across all script mutations, including supporting resources/bootstrap. Only then activate four-engine values, update topology acceptance and deploy with measured failover evidence. Standalone passing readers are not an executable safe migration.
