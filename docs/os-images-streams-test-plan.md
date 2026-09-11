# OS Image Streams Test Plan — Dual RHEL 9/10

| Field                | Value                                                     |
|----------------------|-----------------------------------------------------------|
| **Feature**          | [OCPSTRAT-1150](https://redhat.atlassian.net/browse/OCPSTRAT-1150) — Support two major versions of RHEL CoreOS in a single OCP release |
| **Implementation Epic** | [MCO-2161](https://redhat.atlassian.net/browse/MCO-2161) — Dual stream RHEL 9/10 Phase 3 (MCO implementation work) |
| **OCP Version**      | 5.0                                                       |
| **Status**           | Draft — requires feature/QE owner review before execution |
| **Document Style**   | IEEE 829 (Test Plan)                                      |

## 1. Test Plan Identifier

`MCO-2161-TP-OS-IMAGE-STREAMS`

## 2. References

### Primary

- OCPSTRAT-1150 — Feature: Support two major versions of RHEL CoreOS in a single OCP release
- MCO-2161 — Epic: MCO implementation work for dual-stream support (Phase 3: boot images, agent workflows, GA process)
- MCO-1995 — Epic: MCO implementation work for dual-stream support (Phase 2: OS image stream core)

### Related Work

- OCPSTRAT-2714 — Feature: ABI (Agent-Based Installer) support for RHCOS 10 GA (related)
- AGENT-1468 — Assisted Installer support for new `install-config` OS stream field
- MCO-2214 — Spike: Assisted Service / ZTP manifest integration

### Repository References

- `pkg/osimagestream/` — OS image stream library (29 Go files: 15 non-test, 14 test)
- `pkg/controller/osimagestream/` — OSImageStream controller (reconciliation loop)
- `cmd/machine-config-osimagestream/` — CLI tool for extracting OS image stream data from release payloads
- `pkg/controller/bootimage/` — Boot image controller (MachineSet/CPMS reconciliation)
- `docs/OSUpgrades.md` — Existing documentation on boot images and OS update lifecycle

## 3. Introduction

### 3.1 Purpose

This test plan covers MCO validation for the OCPSTRAT-1150 dual RHEL 9/10
stream feature in OCP 5.0. MCO-2161 is the implementation epic within MCO;
this plan addresses MCO-owned testing only.

The plan covers:

1. **OS image stream selection** — ensuring clusters can be installed and
   operated with either rhel-9 or rhel-10 as the selected OS stream.
2. **Boot image correctness** — verifying that the boot image controller
   resolves the correct platform image (AMI, GCE image, Azure image, OVA) for
   the active OS stream after a stream switch.
3. **Feature persistence across stream switches** — confirming that kernel
   and extension configurations survive an OS stream change.
4. **Agent/installer workflows** (planned/TBD) — validating that the
   Assisted Installer and ZTP manifest path honour the OS stream selection.
5. **GA/release process** (planned/TBD) — confirming that the release
   payload, CVO integration, and upgrade path work correctly for dual-stream
   releases.

### 3.2 Scope Definition

**In scope (agreed dual-stream outcomes for OCP 5.0):**

- A cluster can be installed with a single chosen OS stream (rhel-9 or rhel-10).
- A running cluster's MachineConfigPools can be switched from one stream to
  the other (for example, from rhel-9 to rhel-10).
- Default stream selection follows the cluster version: rhel-9 for OCP 4.x
  origins, rhel-10 for OCP 5.x fresh installs; OKD/SCOS defaults to centos-10.
- After a stream switch, the boot image controller updates platform images to
  match the newly active stream.

**Out of scope / TBD:**

- **Simultaneous mixed RHEL 9 and RHEL 10 streams within a single cluster**
  (different MachineConfigPools running different RHEL major versions
  concurrently) is not confirmed as an agreed 5.0 outcome. This plan does not
  assert or validate that scenario. If feature owners later confirm mixed-stream
  support in 5.0, this plan should be amended with the corresponding test
  coverage.
- **Boot image skew enforcement** is a separate feature with independent test coverage (see Section 7).
- **General boot image controller behavior** (mode management, error reporting,
  Ignition spec upgrades, marketplace handling) pre-dates dual streams and has
  established CI coverage. These tests are listed in Section 13 as baseline
  reference only.

### 3.3 IEEE 829 Structure

The plan follows IEEE 829 structure. Each test scenario is mapped to its
source of truth (Jira issue, source code path, or existing CI coverage).
Test cases are split into baseline/reference coverage (Section 13) and new
planned scenarios (Section 14).

## 4. Test Items

The following software items are within scope for testing:

| Item | Source | Version |
|------|--------|---------|
| OSImageStream CR and controller | `pkg/controller/osimagestream/` | OCP 5.0 |
| OSImageStream library | `pkg/osimagestream/` | OCP 5.0 |
| `machine-config-osimagestream` CLI | `cmd/machine-config-osimagestream/` | OCP 5.0 |
| Boot image controller (stream-aware image selection) | `pkg/controller/bootimage/` | OCP 5.0 |
| `coreos-bootimages` ConfigMap (stream data) | MCO manifests | OCP 5.0 |
| `machine-config-osimageurl` ConfigMap | MCO namespace | OCP 5.0 |
| MachineConfigPool `.spec.osImageStream` field | openshift/api | OCP 5.0 |

## 5. Software Risk Issues

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Boot image mismatch after stream switch causes node provision failure | Medium | High | E2e test scales up MachineSet after boot image update to validate provisioning |
| Agent/installer does not honor new `install-config` OS stream field | Medium | High | AGENT-1468 integration testing (externally owned) |
| Stream default logic selects wrong stream on upgrade from 4.x to 5.x | Low | Critical | Unit tests in `streams_test.go`; e2e validates `GetBuiltinDefaultStreamName` |
| OSImageStream CR has stale data after CVO update | Low | Medium | Controller reconciliation tests; cached inspector tests |
| On-Cluster Layering (OCL) build not triggered when stream changes | Medium | Medium | MOSB trigger test validates new MachineOSBuild is created |
| Extensions incompatible between RHEL 9 and RHEL 10 | Medium | Medium | Cross-stream extension tests filter for compatible extensions |

## 6. Features to Be Tested

### 6.1 OSImageStream Discovery and Defaults

- OSImageStream CR is created with correct `availableStreams` (rhel-9, rhel-10)
- Default stream is `rhel-9` for OCP 4.x clusters, `rhel-10` for OCP 5+ clusters
- OKD/SCOS builds default to `centos-10`
- `installVersion` fallback when ImageStream name cannot be parsed as a version
- User-configured `spec.defaultStream` overrides the builtin default
- Single-stream payloads use the only available stream as default

### 6.2 MachineConfigPool Stream Configuration

- Setting `.spec.osImageStream.name` on a MachineConfigPool selects the stream
- Invalid stream name is rejected by admission webhook
- Empty stream name is rejected by admission webhook
- Custom MCPs inherit stream from worker pool when no explicit stream is set
- Changing stream triggers node update to the target stream's OS image
- Node RHEL version matches the stream major version after update
- `status.osImageStream` reflects the effective stream after update

### 6.3 Stream Switching and Feature Persistence

- Realtime kernel configuration survives stream switch (rhel-9 to rhel-10 and reverse)
- 64k-pages kernel configuration survives stream switch (ARM64 only, rhel-9 to rhel-10 and reverse)
- Extensions configuration survives stream switch (compatible extensions only)
- On-Cluster Layering: MachineOSBuild is triggered when stream changes on OCL-enabled pool

### 6.4 OSImageStream Conflict Handling

- Setting `osImageURL` on a MachineConfig when `osImageStream` is configured degrades the MCP
- Recovery from degraded state when conflicting MachineConfig is removed
- `status.osImageStream` is empty when `osImageURL` override is active
- `status.osImageStream` is empty when MCP is in degraded state
- `status.osImageStream` is restored after recovery from degraded state

### 6.5 Boot Image Selection for Active Stream

This section is limited to boot image behavior that validates the dual-stream
outcome: after a stream selection or switch, the boot image controller must
resolve and apply the correct platform image for the active stream.

- Boot image controller reconciles MachineSet images to match the active OS stream
- `coreos-bootimages` ConfigMap contains boot image data for the active stream
- After stream switch, a scale-up provisions nodes with the correct stream's boot image

> **Note:** General boot image controller behavior (mode management, error
> reporting, Ignition spec upgrades, marketplace image handling) is pre-existing
> functionality with established CI coverage. Those tests are listed as baseline
> reference in Section 13.2; they are not part of the dual-stream test scope.

### 6.6 Agent/Installer Workflows (Planned — Externally Owned)

The following items depend on work tracked outside this repository (AGENT-1468,
MCO-2214, OCPSTRAT-2714). MCO cannot independently verify these; test
scenarios are included for traceability but are marked TBD.

- Assisted Installer honors the new `install-config` OS stream field (AGENT-1468)
- ZTP manifests correctly configure the OS stream in installer code (MCO-2214)
- Agent-based installation (ABI) provisions nodes with the specified OS stream
- Bootstrap node serves correct Ignition for the selected stream

### 6.7 GA/Release Process (Planned)

- Release payload contains both rhel-9 and rhel-10 `rhel-coreos` images
- CVO integration correctly extracts OSImageStream from release payload
- Upgrade from 4.x to 5.0 transitions default stream from rhel-9 to rhel-10
- `machine-config-osimagestream` CLI correctly reads streams from release payload
- ConfigMap URL provider returns correct OS image and extensions image URLs

## 7. Features Not to Be Tested

| Feature | Rationale |
|---------|-----------|
| Simultaneous mixed RHEL 9/10 streams in one cluster | Not confirmed as an agreed 5.0 outcome; requires feature-owner confirmation before test planning |
| Boot image skew enforcement (Manual, Automatic, None modes) | Separate feature (`OCPFeatureGate:BootImageSkewEnforcement`); tested by `test/extended-priv/mco_bootimages_skew.go` independently |
| General boot image controller behavior (modes, error reporting, Ignition upgrades, marketplace) | Pre-existing functionality; not dual-stream specific; covered by baseline CI (see Section 13.2) |
| RHEL CoreOS image build process (coreos-assembler) | Out of MCO scope |
| Machine API provider internals (AWS, GCP, Azure, vSphere actuators) | Tested by machine-api-operator; MCO only consumes the MachineSet API |
| CVO release image verification and GPG signing | Out of MCO scope |
| Ignition spec internals (partitioning, filesystem layout) | Tested by coreos/ignition project |
| OKD/SCOS-specific stream behavior beyond default selection | OKD has a single stream (centos-10); minimal MCO-side variation |
| Network proxy handling for image inspection (`NO_PROXY` support) | Tracked separately as MCO-2016 |
| Multi-architecture cluster boot image coordination | Requires separate multi-arch test infrastructure; tracked independently |

## 8. Approach

### 8.1 Test Levels

| Level | Description | Tooling |
|-------|-------------|---------|
| **Unit** | Stream selection logic, image inspection, caching, helpers | `go test` via `make test-unit` |
| **Bootstrap** | Bootstrap vs controller rendering parity | `test/e2e-bootstrap/` with envtest |
| **E2E (in-repo)** | OSImageStream provider, ConfigMap URL, cached inspector | `test/e2e-2of2/` |
| **E2E (TechPreview)** | OSImageURL conflict, MCP degradation/recovery | `test/e2e-techpreview/` |
| **Extended (privileged)** | Full cluster tests: stream switching, kernels, extensions, boot images | `test/extended-priv/` (Ginkgo) |
| **Manual / Exploratory** | Agent workflows, installer integration, GA release validation | Manual against CI clusters |

### 8.2 Platform Coverage

Platform coverage for boot image validation in the context of dual streams:

| Platform | Boot Image Type | Existing CI | Notes |
|----------|----------------|-------------|-------|
| AWS | AMI | Yes | Region-specific AMI maps |
| GCP | GCE Image | Yes | `projects/rhcos-cloud/global/images` |
| Azure | Marketplace Image (HyperV Gen2) | Yes | No-purchase-plan flow |
| vSphere | OVA Template | Yes | In-place update (same template name) |
| BareMetal | IRI / qcow2 | Partial | IRI e2e exists; legacy qcow2 path tested via provisioning CR |
| Other (OpenStack, Nutanix, etc.) | N/A | No | Boot image update not supported |

> **Note:** The specific platform matrix for dual-stream boot image validation
> is subject to feature/QE owner agreement. The table above reflects existing
> CI infrastructure, not a committed test matrix.

### 8.3 Architecture Coverage

| Architecture | Stream Support | Kernel Tests |
|--------------|---------------|--------------|
| amd64 (x86_64) | rhel-9, rhel-10 | Realtime kernel |
| arm64 (aarch64) | rhel-9, rhel-10 | 64k-pages kernel (not on GCP) |
| s390x | TBD | N/A |
| ppc64le | TBD | N/A |

## 9. Item Pass/Fail Criteria

### 9.1 Unit Tests

- All tests in `pkg/osimagestream/` pass (`go test ./pkg/osimagestream/...`)
- All tests in `pkg/controller/osimagestream/` pass

### 9.2 E2E Tests

- `TestImageStreamProviderCVO` passes (CVO image matches provider output)
- `TestConfigMapUrlProvider` passes (ConfigMap URLs are populated and match)
- `TestCachedInspectorFactory` passes (cache hit with broken factory succeeds)
- `TestOSImageStreamOSImageURL` passes (all 3 recovery sub-cases)

### 9.3 Extended Tests

- Polarion-tagged test cases in the baseline inventory (Section 13) pass on
  their target platforms
- No MCP degradation persists after test cleanup
- No ClusterOperator degradation persists after test cleanup

> **Note:** Overall exit criteria for the feature are proposed in Section 16
> and require feature/QE owner acceptance before they are binding.

## 10. Test Deliverables

| Deliverable | Location | Status |
|-------------|----------|--------|
| This test plan | `docs/os-images-streams-test-plan.md` | Draft |
| Unit tests | `pkg/osimagestream/*_test.go` | Existing |
| Bootstrap tests | `test/e2e-bootstrap/bootstrap_test.go` | Existing |
| E2E tests (in-repo) | `test/e2e-2of2/osimagestream_test.go` | Existing |
| E2E tests (TechPreview) | `test/e2e-techpreview/osimagestreamrender_test.go` | Existing |
| Extended tests (OS streams) | `test/extended-priv/mco_osimagestream.go` | Existing |
| Extended tests (boot images) | `test/extended-priv/mco_bootimages.go` | Existing (baseline reference) |
| Agent workflow test results | TBD (AGENT-1468 / OCPSTRAT-2714) | Planned — externally owned |
| GA release validation report | TBD | Planned |

## 11. Remaining Test Tasks

| Task | Owner | Status |
|------|-------|--------|
| Validate agent/installer `install-config` OS stream field | TBD (AGENT-1468 / OCPSTRAT-2714) | Planned — externally owned |
| ZTP manifest OS stream integration testing | TBD (MCO-2214) | Planned — externally owned |
| Upgrade path testing (4.x to 5.0 stream default transition) | TBD | Planned |
| Confirm mixed-stream scope for 5.0 with feature owner | TBD | Pending feature-owner input |
| s390x / ppc64le architecture stream validation | TBD | TBD |
| `NO_PROXY` support for image inspection (MCO-2016) | TBD | Tracked separately |

## 12. Environmental Needs

### 12.1 Hardware and Infrastructure

- CI clusters on supported platforms (provided by OpenShift CI / Prow)
- ARM64 nodes for 64k-pages kernel tests (not on GCP)

### 12.2 Software Prerequisites

- OCP 5.0 nightly or CI payload with dual-stream support
- `OCPFeatureGate:OSStreams` enabled (for TechPreview-gated tests)
- Pull secret with access to release payload images
- `oc` CLI (recent build from openshift/oc)
- `machine-config-osimagestream` CLI (built from this repo)

### 12.3 Access Requirements

- Cluster-admin access to test clusters
- Registry credentials for image inspection
- Platform-specific credentials where boot image tests are executed (vSphere
  vCenter, Azure subscription, etc.)

## 13. Baseline / Reference Test Coverage

This section lists **existing** automated test coverage that is already running
in CI. These tests are not new work proposed by this plan; they are included
for traceability and as a regression baseline. Polarion IDs and automation
file references are taken directly from the repository source code.

### 13.1 OSImageStream — Existing Extended Tests

Feature gate: `OCPFeatureGate:OSStreams`
File: `test/extended-priv/mco_osimagestream.go`

| Polarion | Scenario | Notes |
|----------|----------|-------|
| 86924 | Validate OS Image Streams value | Checks `availableStreams` in OSImageStream CR |
| 86495 | Check default OS Image Stream | Verifies default stream matches cluster version |
| 87095 | Realtime kernel: rhel-9 to rhel-10 | Disruptive; verifies RT kernel persists across stream switch |
| 89322 | Realtime kernel: rhel-10 to rhel-9 | Disruptive; reverse direction |
| 87096 | 64k-pages kernel: rhel-9 to rhel-10 (ARM64) | Disruptive; not on GCP |
| 89323 | 64k-pages kernel: rhel-10 to rhel-9 (ARM64) | Disruptive; not on GCP |
| 87259 | Extensions: rhel-9 to rhel-10 | Disruptive |
| 89324 | Extensions: rhel-10 to rhel-9 | Disruptive |
| 88366 | osImageStream empty when osImageURL is set | Skipped on disconnected |
| 88814 | Invalid osImageURL degrades MCP and clears osImageStream status | |
| 88122 | osImageStream inheritance for custom MCPs | Disruptive |
| 88203 | MOSB triggered when osImageStream is patched (image-mode) | Disruptive |
| 88365 | osStream + osImageURL MC degrades MCP | Disruptive |

### 13.2 Boot Image Controller — Existing Extended Tests (Baseline Reference)

These tests cover **general boot image controller behavior** that pre-dates
dual-stream support. They are listed here as baseline reference only and are
not part of the dual-stream test scope proposed by this plan.

File: `test/extended-priv/mco_bootimages.go`
Platform labels: `Platform:aws`, `Platform:gce`, `Platform:vsphere`, `Platform:azure`

| Polarion | Scenario |
|----------|----------|
| 81403 | MachineSet boot image updated by default |
| 74240 | ManagedBootImages: restore All MachineSet images |
| 74239 | ManagedBootImages: restore Partial MachineSet images |
| 74751 | ManagedBootImages: fix errors (wrong architecture) |
| 80436 | Boot image secret doesn't exist error |
| 80435 | Boot image no JSON data error |
| 80434 | Boot image wrong Ignition version error |
| 81395 | Boot image update is opt-in by default |
| 80437 | Boot image upgrade stub Ignition to spec 3 |
| 82747 | Correctly handle marketplace boot images |
| 83998 | Multiple labels in architecture annotation |

### 13.3 OSImageStream — Existing E2E Tests

File: `test/e2e-2of2/osimagestream_test.go`

| Test Function | Scenario |
|---------------|----------|
| `TestImageStreamProviderCVO` | CVO release image provides valid ImageStream |
| `TestConfigMapUrlProvider` | ConfigMap URLs populated and match stream data |
| `TestCachedInspectorFactory` | Cache hit succeeds with broken underlying factory |

### 13.4 OSImageStream Conflict — Existing TechPreview Tests

File: `test/e2e-techpreview/osimagestreamrender_test.go`

| Test Function | Scenario |
|---------------|----------|
| `TestOSImageStreamOSImageURL` | Three recovery sub-cases: delete conflicting MC, clear stream from MCP, clear osImageURL from MC |

## 14. Planned Test Scenarios

This section lists **new test scenarios** required to validate the dual-stream
feature outcomes. These are requirements-based and are marked with proposed
identifiers. Automation paths and Polarion IDs do not yet exist for these
scenarios.

### 14.1 Boot Image Selection After Stream Switch (Planned)

These scenarios verify that the boot image controller resolves the correct
platform image after a stream change, which is the key dual-stream boot image
outcome. General boot image controller correctness is covered by the baseline
tests in Section 13.2.

| Proposed ID | Scenario | Expected Result | Status |
|-------------|----------|-----------------|--------|
| PLAN-BOOT-STREAM-01 | Stream switch updates MachineSet boot image | After MCP stream is changed, MachineSet boot image reference is updated to the target stream's image | Planned |
| PLAN-BOOT-STREAM-02 | Scale-up after stream switch provisions correct image | After stream switch + boot image update, scaling MachineSet to +1 provisions a node with the target stream's OS | Planned |
| PLAN-BOOT-STREAM-03 | `coreos-bootimages` ConfigMap reflects active stream | After stream switch, ConfigMap data corresponds to the newly active stream | Planned |

### 14.2 Agent/Installer Workflows (Planned — Externally Owned)

These scenarios depend on work tracked outside this repository. MCO cannot
independently verify them. They are included for traceability to AGENT-1468,
MCO-2214, and OCPSTRAT-2714.

| Proposed ID | Scenario | Expected Result | Status | Tracking |
|-------------|----------|-----------------|--------|----------|
| PLAN-AGENT-01 | `install-config` OS stream field accepted by installer | Installer accepts and uses the specified stream | TBD — externally owned | AGENT-1468 |
| PLAN-AGENT-02 | ABI provisions with specified stream | Nodes boot with the specified stream's boot image | TBD — externally owned | AGENT-1468 |
| PLAN-AGENT-03 | ZTP manifest configures stream correctly | Installer code picks up the stream from ZTP manifest | TBD — externally owned | MCO-2214 |
| PLAN-AGENT-04 | Bootstrap serves correct Ignition for stream | Ignition served to nodes references the correct stream's OS image | TBD | MCO-2161 |
| PLAN-AGENT-05 | Assisted Service configures stream via API | Cluster installed with the requested OS stream | TBD — externally owned | AGENT-1468 |

### 14.3 GA/Release Process (Planned)

These scenarios validate release-level dual-stream outcomes. Some can be
verified with the `machine-config-osimagestream` CLI from this repository;
others require release engineering infrastructure.

| Proposed ID | Scenario | Expected Result | Status |
|-------------|----------|-----------------|--------|
| PLAN-GA-01 | Release payload contains dual-stream images | Both `rhel-coreos` (rhel-9) and `rhel-coreos-10` present in release payload | Planned |
| PLAN-GA-02 | Upgrade 4.x to 5.0 transitions default stream | Default stream transitions from `rhel-9` to `rhel-10` after upgrade | Planned |
| PLAN-GA-03 | CLI reads streams from release payload | `machine-config-osimagestream get osimagestream` returns both streams with correct image references | Planned |
| PLAN-GA-04 | `osImageURL` ConfigMap updated during upgrade | `machine-config-osimageurl` ConfigMap reflects the new release's OS image | Planned |

## 15. Proposed Entry Criteria

> **These criteria are proposed and require acceptance by the OCPSTRAT-1150
> feature owner and QE lead before they become binding.**

- [ ] Test scope, planned scenarios (Section 14), and pass/fail criteria are
      accepted by the OCPSTRAT-1150 feature owner and QE lead
- [ ] A candidate OCP 5.0 build with dual-stream support is available for
      test execution (CI payload or nightly)
- [ ] CI infrastructure and test environment are available for the agreed
      platform/architecture matrix
- [ ] Dependencies are identified: MCO-1995 (Phase 2) merged; externally-owned
      items (AGENT-1468, MCO-2214) have status and contact for coordination
- [ ] Evidence ownership is assigned: who records results, where artifacts are
      stored, and how defects are filed

## 16. Proposed Exit Criteria

> **These criteria are proposed and require acceptance by the OCPSTRAT-1150
> feature owner and QE lead before they become binding.**

- [ ] Every in-scope planned scenario (Section 14) is executed and passing,
      or explicitly marked N/A with documented rationale
- [ ] Test evidence (results, logs, artifacts) is recorded and linked to this
      plan or its tracking issues
- [ ] Every test failure has a filed defect, an approved waiver, or a
      documented disposition
- [ ] The agreed platform/architecture environment matrix (Section 8.2/8.3) is
      completed to the extent accepted by the feature owner
- [ ] OCPSTRAT-1150 feature owner and QE lead sign off that exit criteria are
      satisfied

## 17. Suspension Criteria and Resumption Requirements

### Suspension

- CI infrastructure outage preventing test execution
- Blocking dependency (AGENT-1468, MCO-2214) not available for externally-owned scenarios
- Critical bug requiring architectural changes to OSImageStream subsystem

### Resumption

- CI infrastructure restored
- Blocking dependency available and integrated
- Critical bug fixed and verified

## 18. Traceability Matrix

| Jira Issue | Section | Coverage Area |
|------------|---------|---------------|
| OCPSTRAT-1150 | All | Primary feature: dual RHEL 9/10 stream support |
| MCO-2161 | All | MCO implementation epic (Phase 3) |
| MCO-1995 | 13.1, 13.3, 13.4 | Phase 2 baseline coverage (regression) |
| AGENT-1468 | 14.2 | Agent/installer integration (externally owned) |
| MCO-2214 | 14.2 | ZTP manifest integration (externally owned) |
| OCPSTRAT-2714 | 14.2 | Related: ABI RHCOS 10 support (externally owned) |

## 19. Approvals

| Role | Name | Date | Signature |
|------|------|------|-----------|
| Test Plan Author | TBD | TBD | |
| MCO Tech Lead | TBD | TBD | |
| QE Lead | TBD | TBD | |
| OCPSTRAT-1150 Feature Owner | TBD | TBD | |
