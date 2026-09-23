# FG-04 Linux platform staging adapter

This directory implements a deliberately inactive Linux IPVS Direct Routing
adapter. It does not deploy a frontend, elect an owner, implement a kernel fence,
or establish platform/HA acceptance. Existing kw manifests are untouched.

## Contract and interface

`Adapter(manifest, runner).plan()` reads the actual namespace resources and returns
the exact argv operations needed to converge. It performs no writes.
`reconcile(fence)` executes those operations and checks convergence.
`reconcile(fence, cleanup=True)` deletes only recognized owned resources and checks
cleanup convergence. Calls are synchronous; command errors/timeouts abort without
rollback, retain the external fence, and leave kernel state available for recovery.
A new adapter with the identical manifest can resume or clean up partial work.

The injectable `Runner` implements `run(argv)` and `inode(namespace)`.
`LinuxRunner` uses fixed `/usr/sbin/ip`, `/usr/sbin/ipvsadm`, `/usr/sbin/sysctl`,
bounded subprocess calls and no shell. Missing tools, kernel APIs, privileges,
unsupported output or unexpected resources fail closed. Module loading is not an
installation step: the IPVS sysctl must exist before invoking ipvsadm. IPVS RR,
dummy and attachment support must already be available on the target kernel;
unsupported mutations stop under the external fence.
The required module names must be visible under `/sys/module`; kernels hiding
those capability markers are conservatively unsupported, including built-in
configurations without the corresponding visible markers.

`Fence.run_closed(manifest, callback)` is the integration boundary owned by the
separate kernel-fence lane. The provider must validate the exact manifest identity
and pinned namespaces, serialize all writers, and establish a kernel-enforced
traffic **and advertisement** deny before calling the callback. Denial must survive
exceptions, command timeouts, adapter death, and lease loss. It must remain denied
when the callback returns. A mutex, lease or boolean is insufficient. This directory
ships no production provider and imports nothing from `deploy/failover/fence`.
The Python protocol describes a trusted integration contract, not proof that an
arbitrary supplied implementation actually enforces it.

Activation is exclusively an external operation. This adapter never adds a
frontend VIP address, brings a VIP link up, enables forwarding, enables an IPVS
destination, emits advertisements, or terminates TLS. Every destination uses `-g`
(DR) and weight zero. Frontend services are exactly UDP/53, TCP/53, TCP/853, TCP/443
and UDP/853. Backend VIPs use an owned down dummy link; ARP suppression is a checked
precondition. This preserves the intended tuple contract structurally, but packet
tuple preservation by this implementation still needs Linux/engine evidence.

Active state is intentionally rejected. Withdrawal and normalization back to this
staged contract must occur under the external fence before staging cleanup. This
is not yet a complete active-frontend update/delete API or eligibility reconciler.

## Explicit attachment and ownership manifest

See [example.manifest.json](example.manifest.json). Its inode/index/address values
are illustrative; it cannot be used against real attachments without provisioning
and independent inventory. IPv4 only; unknown fields and duplicate JSON keys fail.
The two supported mappings are `.136` / A,C and `.139` / B,D. Exactly two unique
persistent engine UID/address bindings and the exact five service entries are
required, including on a host with only one local backend attachment.

Each manifest contains one to three **local** roles: `frontend` and/or its group
members. Remote members remain explicit bindings; they are not operated through
SSH or inferred from Cilium. A trusted pre-provisioner must create the dedicated
named namespaces and veth/macvlan attachments, inventory inode, ifindex, MAC, MTU,
IPv4 CIDR and kind, then set the following link aliases using `identity(manifest)`:

- Namespace `lo`: `<identity>:namespace:<role>`.
- Dedicated attachment: `<identity>:attachment:<role>`.
- Pre-provisioned DOWN dummy `nxvip`: `<identity>:vip:<role>`.

The provisioner owns fresh isolated namespaces exclusively until handover. Create
`nxvip` without an alias, then explicitly set its alias with `ip -n <namespace>
link set dev nxvip alias <identity>:vip:<role>` after finalizing the manifest.
Read back and verify every marker and the empty DOWN dummy before publishing the
manifest to a reconciler. Alias-on-create is **not** assumed atomic or effective:
the parent's Linux run returned success but a dummy without `ifalias`. On any
provisioning or verification failure, the creator must destroy its fresh owned
namespace; it must not publish a partially prepared manifest or ask reconciliation
to adopt the unmarked link. Process-death recovery remains the creator's duty; the
isolated test loses its private namespace mounts/network on child termination.

Identity includes the group UID, run ID and SHA-256 of the full canonical manifest.
Any changed manifest/run requires an explicit provisioning/recovery decision;
there is no force/adopt mode or automatic identity migration. Keep the original
manifest for interrupted-work recovery. Markers scope a trusted provisioner's
allocation; they do not authenticate a hostile root or attest engine identity.
FG-02 must provide independently trusted engine binding/placement evidence.

Namespaces, dedicated attachments and VIP dummies are pre-provisioned resources. Their
reconciliation consists of exact existence/identity/configuration validation;
this adapter never creates, renames, moves, adopts or deletes them. Missing or
replaced ones fail before any mutation. Only the exact backend VIP `/32` address, routes, rule and IPVS objects are
created/deleted. Cleanup removes the address after dependent routes/rules and
preserves the owned DOWN dummy. Reconciliation never creates, marks or deletes a
VIP link, including an absent, unmarked or foreign dummy. These states fail closed
for both staging and cleanup before any mutation across all attachments.

Only `lo`, the manifest attachment and required owned DOWN dummy may exist in each
namespace. The attachment must be UP with exactly its declared IPv4 address and
no IPv6 addresses. Provision namespace `all` and attachment `arp_ignore=1`,
`arp_announce=2`, `rp_filter=0`, plus `ip_forward=0`; disable IPv6 before provisioning
addresses. The adapter checks but does not rewrite these settings. This strict
topology deliberately rejects the real-engine fixture's additional management
link: extending the manifest to permit that attachment is a separate reviewed
integration step, not an implicit production Cilium attachment.

Routes are explicit and local to the pinned namespace:

- Frontend: one on-link backend `/32` in main per binding, protocol 186.
- Backend: declared gateway `/32` link routes plus declared client return CIDRs
  via those gateways in table 186; preferred source is the stable group VIP.
  A priority-186 source rule selects that table for the group VIP.
- Only exact automatic kernel routes for declared addresses/loopback, standard
  policy rules and the owned routes/rule are allowed. All tables are inspected;
  no defaults, foreign static routes, route guessing or flush operations.

Protocol 186 is rendered `bgp` by some iproute2 versions; both canonical forms are
recognized. It is only a tag inside an exclusively assigned namespace, not an
ownership claim over host BGP routes. Backend return CIDRs must be fully declared;
this adapter does not prove coverage of all clients or independently attest L2,
anti-spoofing, tc/nft state, gateway reachability or failure-domain separation.
Those remain pre-provisioner/fence/acceptance responsibilities.

## Read-only CLI and tests

From the repository root, no privileges or Linux are needed to validate a file:

```sh
python3 -B deploy/failover/platform/cmd/plan.py deploy/failover/platform/example.manifest.json
```

Add `--check` on the intended Linux host to inspect resources and print exact
pending operations. The CLI has no apply, cleanup or activation option.

Run deterministic command-injection tests without privileges:

```sh
python3 -B -m unittest discover -s deploy/failover/platform -v
ruff check deploy/failover/platform
ruff format --check deploy/failover/platform
```

Parent-owned isolated Linux execution, explicitly opt-in:

```sh
sudo env NEXORA_FG04_ISOLATED_TEST=yes PYTHONDONTWRITEBYTECODE=1 \
  python3 deploy/failover/platform/cmd/linux_namespace_test.py --execute
```

Requirements: Linux root with namespace/mount capabilities; util-linux `unshare`
and `mount` at `/usr/bin`; fixed binaries above; existing `/run/netns`; preloaded
IPVS/RR, veth and dummy support. No module, package or trust installation occurs.
The runner creates private mount/network/PID namespaces, verifies inherited parent
network/mount descriptors and PID isolation before mounting a private `/run/netns`,
then creates three test namespaces with detached, down outer veth peers. It uses
the stable VIP only inside this disconnected topology. The test fence is valid
solely for that unreachable topology and is not an HA/kernel-fence implementation.
On parent timeout, unshare kills the isolated child. PASS is printed only after
owned namespace cleanup. Missing capabilities fail rather than skip or substitute
another datapath. Save original stdout/stderr and exit status; do not overwrite a
failed run with a retry.

The Linux test exercises real stage, repeated stage, new-instance recovery and
exact cleanup. Unit tests additionally inject failure before and after every
apply/cleanup operation, including partial cleanup, and reject foreign state.
Neither test starts Nexora or proves packet forwarding, policy, management, HA,
or production attachment. See [HANDOFF.md](HANDOFF.md) for actual evidence/gaps.
