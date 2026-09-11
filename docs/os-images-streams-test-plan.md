# OS Image Streams Test Plan -- Dual RHEL 9/10 Phase 3

| Field               | Value                                              |
|---------------------|----------------------------------------------------|
| **Jira Epic**       | [MCO-2161](https://issues.redhat.com/browse/MCO-2161) |
| **Parent Strategy** | OCPSTRAT-1150                                      |
| **OCP Version**     | 5.0                                                |
| **Status**          | Draft                                              |
| **Document Style**  | IEEE 829 (Test Plan)                               |

## 1. Test Plan Identifier

`MCO-2161-TP-OS-IMAGE-STREAMS-PHASE3`

## 2. References

- MCO-2161 -- Dual stream RHEL9/10 Phase 3 (boot images, agent workflows, GA process)
- MCO-1995 -- Dual stream RHEL9/10 Phase 2 (predecessor; OS image stream core implementation)
- OCPSTRAT-1150 -- Strategic initiative for dual RHEL 9/10 streams
- OCPSTRAT-2714 -- Related strategy for OS stream lifecycle
- AGENT-1468 -- Assisted Installer support for new `install-config` field
- MCO-2214 -- Spike: Assisted Service / ZTP manifest integration
- `pkg/osimagestream/` -- OS image stream library (stream discovery, image inspection, CVO integration)
- `pkg/controller/osimagestream/` -- OSImageStream controller (reconciliation loop)
- `cmd/machine-config-osimagestream/` -- CLI tool for extracting OS image stream data from release payloads
- `docs/OSUpgrades.md` -- Existing documentation on boot images vs rhel-coreos and OS update lifecycle

## 3. Introduction

This test plan covers the **Phase 3** scope of the dual RHEL 9/10 OS image
stream feature in the Machine Config Operator (MCO). Phase 3 extends Phase 2
(MCO-1995) by adding:

1. **Boot image management** -- ensuring boot images (AMIs, VMDKs, qcow2s, Azure
   marketplace images) are correctly resolved and updated when multiple OS
   streams (rhel-9, rhel-10) are available.
2. **Agent/installer workflows** -- validating that the Assisted Installer (ABI),
   ZTP manifests, and the `install-config` field correctly interact with the OS
   stream selection.
3. **GA/release process** -- confirming that the release payload, CVO
   integration, and upgrade path work correctly for dual-stream clusters
   shipping to GA.

The plan follows IEEE 829 structure and maps each test scenario to its source
of truth (Jira issue, source code, or existing CI coverage).

## 4. Test Items

The following software items are within scope for testing:

| Item | Source | Version |
|------|--------|---------|
| OSImageStream CR and controller | `pkg/controller/osimagestream/` | OCP 5.0 |
| OSImageStream library | `pkg/osimagestream/` (29 source files) | OCP 5.0 |
| `machine-config-osimagestream` CLI | `cmd/machine-config-osimagestream/` | OCP 5.0 |
| Boot image controller (MachineSet/CPMS reconciliation) | `pkg/controller/bootimage/` | OCP 5.0 |
| Boot image skew enforcement controller | `pkg/controller/bootimage/`, `pkg/operator/` | OCP 5.0 |
| `coreos-bootimages` ConfigMap (stream data) | MCO manifests | OCP 5.0 |
| `machine-config-osimageurl` ConfigMap | MCO namespace | OCP 5.0 |
| MachineConfigPool `.spec.osImageStream` field | openshift/api | OCP 5.0 |
| MachineConfiguration `.spec.managedBootImages` | openshift/api | OCP 5.0 |

## 5. Software Risk Issues

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Boot image mismatch after stream switch causes node provision failure | Medium | High | E2e test scales up MachineSet after boot image update to validate provisioning |
| Agent/installer does not honor new `install-config` OS stream field | Medium | High | AGENT-1468 integration testing; ZTP manifest validation (MCO-2214) |
| Stream default logic selects wrong stream on upgrade from 4.x to 5.x | Low | Critical | Unit tests in `streams_test.go`; e2e validates `GetBuiltinDefaultStreamName` |
| Boot image skew enforcement blocks upgrade when streams are mismatched | Medium | High | Skew enforcement e2e tests (Manual, Automatic, None modes) |
| OSImageStream CR has stale data after CVO update | Low | Medium | Controller reconciliation tests; cached inspector tests |
| vSphere in-place template update corrupts template during stream switch | Low | High | Platform-specific e2e with OVA upload and release version verification |
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

### 6.2 Boot Image Management Across Streams

- Boot image controller reconciles MachineSet images to match the active OS stream
- `coreos-bootimages` ConfigMap contains per-architecture, per-platform boot image data
- Boot image update works for all supported platforms: AWS, GCP, Azure, vSphere
- Managed boot images modes: All, Partial (label-based), None (opt-out)
- MachineSet with owner references is excluded from boot image updates
- User-data secret Ignition spec is upgraded to v3 during boot image reconciliation
- Marketplace boot images are correctly handled (AWS, GCP exemptions)
- Boot image controller reports degraded status with actionable error messages
- CPMS (ControlPlaneMachineSet) boot image reconciliation on supported platforms

### 6.3 Boot Image Skew Enforcement

- Automatic mode: MachineSet boot images are kept current; CO is upgradeable
- Manual mode with RHCOSVersion: upgradeable when version is within skew limits
- Manual mode with OCPVersion: upgradeable when version is within skew limits
- Automatic mode sad path: CO not upgradeable when boot image controller is degraded
- None mode: skew enforcement disabled; CO always upgradeable
- BareMetal: defaults to None mode; flips to Manual on legacy qcow2 provisioning path
- Automatic mode permits CPMS-only managers but rejects non-All MachineSet modes

### 6.4 MachineConfigPool Stream Configuration

- Setting `.spec.osImageStream.name` on a MachineConfigPool selects the stream
- Invalid stream name is rejected by admission webhook
- Empty stream name is rejected by admission webhook
- Custom MCPs inherit stream from worker pool when no explicit stream is set
- Changing stream triggers node update to the target stream's OS image
- Node RHEL version matches the stream major version after update
- `status.osImageStream` reflects the effective stream after update

### 6.5 Cross-Stream Feature Persistence

- Realtime kernel configuration survives stream switch (rhel-9 to rhel-10 and reverse)
- 64k-pages kernel configuration survives stream switch (ARM64 only, rhel-9 to rhel-10 and reverse)
- Extensions configuration survives stream switch (compatible extensions only)
- On-Cluster Layering: MachineOSBuild is triggered when stream changes on OCL-enabled pool

### 6.6 OSImageStream Conflict Handling

- Setting `osImageURL` on a MachineConfig when `osImageStream` is configured degrades the MCP
- Recovery from degraded state when conflicting MachineConfig is removed
- `status.osImageStream` is empty when `osImageURL` override is active
- `status.osImageStream` is empty when MCP is in degraded state
- `status.osImageStream` is restored after recovery from degraded state

### 6.7 Agent/Installer Workflows (Phase 3 Scope)

- Assisted Installer honors the new `install-config` OS stream field (AGENT-1468)
- ZTP manifests correctly configure the OS stream in installer code (MCO-2214)
- Agent-based installation (ABI) provisions nodes with the specified OS stream
- Bootstrap node serves correct Ignition for the selected stream

### 6.8 GA/Release Process

- Release payload contains both rhel-9 and rhel-10 `rhel-coreos` images
- CVO integration correctly extracts OSImageStream from release payload
- Upgrade from 4.x to 5.0 transitions default stream from rhel-9 to rhel-10
- `machine-config-osimagestream` CLI correctly reads streams from release payload
- ConfigMap URL provider returns correct OS image and extensions image URLs

## 7. Features Not to Be Tested

| Feature | Rationale |
|---------|-----------|
| RHEL CoreOS image build process (coreos-assembler) | Owned by CoreOS team; out of MCO scope |
| Machine API provider internals (AWS, GCP, Azure, vSphere actuators) | Tested by machine-api-operator; MCO only consumes the MachineSet API |
| CVO release image verification and GPG signing | Owned by cluster-version-operator |
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
| **Extended (privileged)** | Full cluster tests: boot images, skew, stream switching, kernels, extensions | `test/extended-priv/` (Ginkgo) |
| **Manual / Exploratory** | Agent workflows, installer integration, GA release validation | Manual against CI clusters |

### 8.2 Platform Coverage

| Platform | Boot Image Type | Automated | Notes |
|----------|----------------|-----------|-------|
| AWS | AMI | Yes | Region-specific AMI maps |
| GCP | GCE Image | Yes | `projects/rhcos-cloud/global/images` |
| Azure | Marketplace Image (HyperV Gen2) | Yes | No-purchase-plan flow |
| vSphere | OVA Template | Yes | In-place update (same template name) |
| BareMetal | IRI / qcow2 | Partial | IRI e2e exists; legacy qcow2 path tested via provisioning CR |
| Other (OpenStack, Nutanix, etc.) | N/A | No | Boot image update not supported |

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
- All Polarion-tagged test cases pass on their target platforms
- No MCP degradation persists after test cleanup
- No ClusterOperator degradation persists after test cleanup

### 9.4 Overall Exit Criteria
- Zero P0/P1 bugs open against MCO-2161 Phase 3 scope
- All automated test cases pass in CI for at least 2 consecutive payload acceptances
- Boot image update verified on at least AWS, GCP, Azure, and vSphere
- Agent workflow integration validated (AGENT-1468 scope)

## 10. Test Deliverables

| Deliverable | Location | Status |
|-------------|----------|--------|
| This test plan | `docs/os-images-streams-test-plan.md` | Draft |
| Unit tests | `pkg/osimagestream/*_test.go` | Existing |
| Bootstrap tests | `test/e2e-bootstrap/bootstrap_test.go` | Existing |
| E2E tests (in-repo) | `test/e2e-2of2/osimagestream_test.go` | Existing |
| E2E tests (TechPreview) | `test/e2e-techpreview/osimagestreamrender_test.go` | Existing |
| Extended tests (boot images) | `test/extended-priv/mco_bootimages.go` | Existing |
| Extended tests (skew) | `test/extended-priv/mco_bootimages_skew.go` | Existing |
| Extended tests (OS streams) | `test/extended-priv/mco_osimagestream.go` | Existing |
| Agent workflow test results | TBD (AGENT-1468) | Pending |
| GA release validation report | TBD | Pending |

## 11. Remaining Test Tasks

| Task | Owner | Status |
|------|-------|--------|
| Validate agent/installer `install-config` OS stream field | TBD (AGENT-1468) | Pending |
| ZTP manifest OS stream integration testing | TBD (MCO-2214) | Pending |
| Upgrade path testing (4.x to 5.0 stream default transition) | TBD | Pending |
| s390x / ppc64le architecture stream validation | TBD | Pending |
| `NO_PROXY` support for image inspection (MCO-2016) | TBD | Tracked separately |

## 12. Environmental Needs

### 12.1 Hardware and Infrastructure

- CI clusters on AWS, GCP, Azure, vSphere (provided by OpenShift CI / Prow)
- BareMetal cluster for IRI and provisioning path tests
- ARM64 nodes for 64k-pages kernel tests (not on GCP)
- Multi-architecture cluster infrastructure (for future multi-arch validation)

### 12.2 Software Prerequisites

- OCP 5.0 nightly or CI payload with dual-stream support
- `OCPFeatureGate: OSStreams` enabled (for TechPreview-gated tests)
- `OCPFeatureGate: BootImageSkewEnforcement` enabled (for skew tests)
- Pull secret with access to release payload images
- `oc` CLI (recent build from openshift/oc)
- `machine-config-osimagestream` CLI (built from this repo)

### 12.3 Access Requirements

- Cluster-admin access to test clusters
- Registry credentials for image inspection
- vSphere vCenter credentials (for OVA upload tests)
- Azure subscription (for marketplace image tests)

## 13. Test Cases

### 13.1 OSImageStream Discovery

| ID | Scenario | Steps | Expected Result | Automation | Polarion |
|----|----------|-------|-----------------|------------|----------|
| TP3-DISC-01 | OSImageStream CR contains expected streams | Get OSImageStream CR; check `status.availableStreams` | Contains `rhel-9` and `rhel-10` entries | `mco_osimagestream.go` | 86924 |
| TP3-DISC-02 | Default stream matches cluster version | Get OSImageStream CR; check `status.defaultStream` | `rhel-10` for OCP 5.x; `rhel-9` for OCP 4.x | `mco_osimagestream.go` | 86495 |
| TP3-DISC-03 | ImageStreamProvider reads from CVO release image | Run `TestImageStreamProviderCVO` | Provider returns non-empty ImageStream with tags | `osimagestream_test.go` | N/A |
| TP3-DISC-04 | ConfigMap URL provider returns correct URLs | Run `TestConfigMapUrlProvider` | `osImageURL` and `osExtensionsImageURL` match ConfigMap data | `osimagestream_test.go` | N/A |
| TP3-DISC-05 | Cached inspector serves from cache on second call | Run `TestCachedInspectorFactory` | Second inspect with broken factory succeeds from cache | `osimagestream_test.go` | N/A |
| TP3-DISC-06 | CLI `get osimagestream` returns valid JSON | Run `machine-config-osimagestream get osimagestream --release-image <payload>` | Valid JSON with `spec` and `status` fields | Manual / CI script | N/A |

### 13.2 MachineConfigPool Stream Configuration

| ID | Scenario | Steps | Expected Result | Automation | Polarion |
|----|----------|-------|-----------------|------------|----------|
| TP3-MCP-01 | Invalid stream name rejected | Patch MCP with `osImageStream.name = "rhel-10-invalid"` | Admission webhook rejects with validation error | `mco_osimagestream.go` | 86924 |
| TP3-MCP-02 | Empty stream name rejected | Patch MCP with `osImageStream.name = ""` | Admission webhook rejects (min 1 char) | `mco_osimagestream.go` | 86924 |
| TP3-MCP-03 | Custom MCP inherits stream from worker pool | Create custom MCP without `osImageStream`; verify node uses worker pool stream | Node runs OS image matching worker pool's effective stream | `mco_osimagestream.go` | 88122 |
| TP3-MCP-04 | Custom MCP with explicit stream overrides inheritance | Create custom MCP with `osImageStream = <target>`; verify node uses target stream | Node runs OS image matching the explicitly set stream | `mco_osimagestream.go` | 88122 |
| TP3-MCP-05 | Dynamic inheritance after worker pool stream change | Create custom MCP (no stream); patch worker pool to target stream; add node to custom MCP | Custom MCP node inherits the updated worker pool stream | `mco_osimagestream.go` | 88122 |
| TP3-MCP-06 | `status.osImageStream` reflects effective stream | Set stream on MCP; wait for update; check `status.osImageStream` | Status field matches the configured stream name | `mco_osimagestream.go` | 86495 |

### 13.3 Cross-Stream Feature Persistence

| ID | Scenario | Steps | Expected Result | Automation | Polarion |
|----|----------|-------|-----------------|------------|----------|
| TP3-XSTREAM-01 | Realtime kernel: rhel-9 to rhel-10 | Configure RT kernel on rhel-9; switch to rhel-10; verify RT kernel persists | RT kernel active on rhel-10; RHEL version starts with "10." | `mco_osimagestream.go` | 87095 |
| TP3-XSTREAM-02 | Realtime kernel: rhel-10 to rhel-9 | Configure RT kernel on rhel-10; switch to rhel-9; verify RT kernel persists | RT kernel active on rhel-9; RHEL version starts with "9." | `mco_osimagestream.go` | 89322 |
| TP3-XSTREAM-03 | 64k-pages kernel: rhel-9 to rhel-10 (ARM64) | Configure 64k kernel on rhel-9; switch to rhel-10; verify kernel persists | 64k kernel active on rhel-10 (ARM64 only, not GCP) | `mco_osimagestream.go` | 87096 |
| TP3-XSTREAM-04 | 64k-pages kernel: rhel-10 to rhel-9 (ARM64) | Configure 64k kernel on rhel-10; switch to rhel-9; verify kernel persists | 64k kernel active on rhel-9 (ARM64 only, not GCP) | `mco_osimagestream.go` | 89323 |
| TP3-XSTREAM-05 | Extensions: rhel-9 to rhel-10 | Configure compatible extensions on rhel-9; switch to rhel-10; verify installed | Extensions RPMs present after stream switch | `mco_osimagestream.go` | 87259 |
| TP3-XSTREAM-06 | Extensions: rhel-10 to rhel-9 | Configure compatible extensions on rhel-10; switch to rhel-9; verify installed | Extensions RPMs present after stream switch | `mco_osimagestream.go` | 89324 |
| TP3-XSTREAM-07 | OCL MOSB triggered on stream change | Enable OCL on custom MCP; change stream; verify new MachineOSBuild | New MOSB name differs from initial; build succeeds | `mco_osimagestream.go` | 88203 |

### 13.4 OSImageStream Conflict Handling

| ID | Scenario | Steps | Expected Result | Automation | Polarion |
|----|----------|-------|-----------------|------------|----------|
| TP3-CONFLICT-01 | osImageURL + osImageStream degrades MCP | Set stream on MCP; apply MC with osImageURL; wait for degradation | MCP `RenderDegraded=True` with conflict message | `mco_osimagestream.go` | 88365 |
| TP3-CONFLICT-02 | Recovery by deleting conflicting MC | Delete the osImageURL MC | MCP `RenderDegraded=False` | `mco_osimagestream.go` | 88365 |
| TP3-CONFLICT-03 | osImageStream empty when osImageURL active | Apply osImageURL MC (no stream set); check `status.osImageStream` | Status field is empty | `mco_osimagestream.go` | 88366 |
| TP3-CONFLICT-04 | osImageStream empty when MCP degraded | Apply invalid MC to degrade MCP; check `status.osImageStream` | Status field is empty | `mco_osimagestream.go` | 88814 |
| TP3-CONFLICT-05 | osImageStream restored after recovery | Delete invalid MC; wait for recovery; check `status.osImageStream` | Status field restored to effective stream | `mco_osimagestream.go` | 88814 |
| TP3-CONFLICT-06 | OSImageURL override with delete recovery | Set stream + create osImageURL MC; delete MC to recover | Pool recovers from degraded; stream rendering resumes | `osimagestreamrender_test.go` | N/A |
| TP3-CONFLICT-07 | OSImageURL override with stream clear recovery | Set stream + create osImageURL MC; clear stream from MCP | Pool recovers from degraded | `osimagestreamrender_test.go` | N/A |
| TP3-CONFLICT-08 | OSImageURL override with URL clear recovery | Set stream + create osImageURL MC; clear osImageURL from MC | Pool recovers from degraded | `osimagestreamrender_test.go` | N/A |

### 13.5 Boot Image Management

| ID | Scenario | Steps | Expected Result | Automation | Polarion |
|----|----------|-------|-----------------|------------|----------|
| TP3-BOOT-01 | MachineSet boot image updated by default | Backdated boot image in MachineSet; verify MCO updates it | MachineSet boot image matches current `coreos-bootimages` ConfigMap value | `mco_bootimages.go` | 81403 |
| TP3-BOOT-02 | All mode: all MachineSets updated | Configure `managedBootImages` mode All; backdate multiple MachineSets | All MachineSets (without owners) are updated | `mco_bootimages.go` | 74240 |
| TP3-BOOT-03 | Partial mode: only labeled MachineSets updated | Configure Partial mode with label selector; backdate labeled and unlabeled MachineSets | Only labeled MachineSets (without owners) are updated | `mco_bootimages.go` | 74239 |
| TP3-BOOT-04 | None mode: no MachineSets updated | Configure None mode; backdate MachineSet | MachineSet retains the backdated image | `mco_bootimages.go` | 81403 |
| TP3-BOOT-05 | Owner-referenced MachineSet excluded | Add fake owner to MachineSet; backdate; label for update | MachineSet with owner is not updated | `mco_bootimages.go` | 74240 |
| TP3-BOOT-06 | Error reporting: wrong architecture | Set invalid architecture annotation on MachineSet | `BootImageUpdateDegraded=True` with architecture error message | `mco_bootimages.go` | 74751 |
| TP3-BOOT-07 | Error reporting: missing user-data secret | Set non-existent user-data secret on MachineSet | `BootImageUpdateDegraded=True` with secret-not-found message | `mco_bootimages.go` | 80436 |
| TP3-BOOT-08 | Error reporting: invalid user-data JSON | Set non-JSON user-data in secret | `BootImageUpdateDegraded=True` with unmarshal error | `mco_bootimages.go` | 80435 |
| TP3-BOOT-09 | Error reporting: wrong Ignition version | Set unsupported Ignition version in user-data | `BootImageUpdateDegraded=True` with version error | `mco_bootimages.go` | 80434 |
| TP3-BOOT-10 | Ignition spec upgrade to v3 | Set Ignition v2.2.0 user-data; backdate boot image; label | User-data upgraded to latest Ignition version; boot image updated | `mco_bootimages.go` | 80437 |
| TP3-BOOT-11 | Marketplace images handled correctly | Set non-updateable (marketplace) image; label for update | Boot image and user-data are not updated | `mco_bootimages.go` | 82747 |
| TP3-BOOT-12 | Multiple labels in architecture annotation | Set comma-separated labels in capacity annotation | `BootImageUpdateDegraded=False`; no errors reported | `mco_bootimages.go` | 83998 |
| TP3-BOOT-13 | Default opt-in mode verified | Check MachineConfiguration default `managedBootImages` | Status reflects current spec mode (All by default) | `mco_bootimages.go` | 81395 |
| TP3-BOOT-14 | Scale-up after boot image fix | Update backdated MachineSet; scale to 1; wait for node ready | Node provisions successfully with updated boot image | `mco_bootimages.go` | 74240 |

### 13.6 Boot Image Skew Enforcement

| ID | Scenario | Steps | Expected Result | Automation | Polarion |
|----|----------|-------|-----------------|------------|----------|
| TP3-SKEW-01 | Manual/RHCOSVersion within limits | Set manual skew with current RHCOS version | CO upgradeable=True | `mco_bootimages_skew.go` | N/A |
| TP3-SKEW-02 | Manual/RHCOSVersion exceeds limits | Set manual skew with old RHCOS version | CO upgradeable=False | `mco_bootimages_skew.go` | N/A |
| TP3-SKEW-03 | Manual/OCPVersion within limits | Set manual skew with current OCP version limit | CO upgradeable=True | `mco_bootimages_skew.go` | N/A |
| TP3-SKEW-04 | Manual/OCPVersion exceeds limits | Set manual skew with old OCP version | CO upgradeable=False | `mco_bootimages_skew.go` | N/A |
| TP3-SKEW-05 | Automatic mode happy path | Remove skew config; backdate and restore MachineSet | CO upgradeable=True after restoration | `mco_bootimages_skew.go` | N/A |
| TP3-SKEW-06 | Automatic mode sad path | Remove skew config; set broken user-data and backdate | CO upgradeable=False while degraded | `mco_bootimages_skew.go` | N/A |
| TP3-SKEW-07 | Automatic mode permits CPMS managers | Configure CPMS-only or CPMS+All MachineSet managers | Config accepted; MachineSet non-All modes rejected | `mco_bootimages_skew.go` | N/A |
| TP3-SKEW-08 | None mode | Set None skew enforcement | CO upgradeable=True regardless of boot image state | `mco_bootimages_skew.go` | N/A |
| TP3-SKEW-09 | BareMetal default and legacy flip | Check default mode (None); patch provisioning CR with qcow2 URL | Mode flips to Manual; reverts to None on URL removal | `mco_bootimages_skew.go` | N/A |

### 13.7 Agent/Installer Workflows (Phase 3 -- Pending)

| ID | Scenario | Steps | Expected Result | Automation | Tracking |
|----|----------|-------|-----------------|------------|----------|
| TP3-AGENT-01 | `install-config` OS stream field accepted | Create `install-config.yaml` with OS stream field; run installer | Installer accepts and uses the specified stream | TBD | AGENT-1468 |
| TP3-AGENT-02 | ABI provisions with specified stream | Run agent-based installation with OS stream field | Nodes boot with the specified stream's boot image | TBD | AGENT-1468 |
| TP3-AGENT-03 | ZTP manifest configures stream correctly | Apply ZTP manifests with OS stream configuration | Installer code picks up the stream from ZTP manifest | TBD | MCO-2214 |
| TP3-AGENT-04 | Bootstrap serves correct Ignition for stream | During install, inspect bootstrap Ignition served to nodes | Ignition references the correct stream's OS image | TBD | MCO-2161 |
| TP3-AGENT-05 | Assisted Service configures stream via API | Use Assisted Service API to set OS stream preference | Cluster installed with the requested OS stream | TBD | AGENT-1468 |

### 13.8 GA/Release Process (Phase 3 -- Pending)

| ID | Scenario | Steps | Expected Result | Automation | Tracking |
|----|----------|-------|-----------------|------------|----------|
| TP3-GA-01 | Release payload contains dual-stream images | Inspect release payload with `oc adm release info` | Both `rhel-coreos` (rhel-9) and `rhel-coreos-10` present | TBD | MCO-2161 |
| TP3-GA-02 | Upgrade 4.x to 5.0 transitions default stream | Upgrade cluster from 4.x to 5.0; check default stream | Default transitions from `rhel-9` to `rhel-10` | TBD | MCO-2161 |
| TP3-GA-03 | CLI reads streams from release payload | Run `machine-config-osimagestream get osimagestream` against GA payload | Both streams present with correct image references | Manual | N/A |
| TP3-GA-04 | `osImageURL` ConfigMap updated during upgrade | Monitor `machine-config-osimageurl` ConfigMap during upgrade | ConfigMap reflects the new release's OS image | TBD | MCO-2161 |

## 14. Entry Criteria

- [ ] OCP 5.0 CI payloads are building and being accepted
- [ ] `OSStreams` feature gate is available in TechPreview or GA
- [ ] Dual-stream release payload contains both rhel-9 and rhel-10 images
- [ ] MCO-1995 (Phase 2) is complete and merged
- [ ] AGENT-1468 integration code is available for testing
- [ ] CI infrastructure supports AWS, GCP, Azure, and vSphere platforms

## 15. Exit Criteria

- [ ] All automated test cases (Sections 13.1 -- 13.6) pass in CI
- [ ] Agent workflow test cases (Section 13.7) validated -- at minimum TP3-AGENT-01 and TP3-AGENT-02
- [ ] GA release process test cases (Section 13.8) validated -- at minimum TP3-GA-01 and TP3-GA-02
- [ ] No P0 or P1 bugs open against MCO-2161
- [ ] Boot image update verified on all 4 supported platforms (AWS, GCP, Azure, vSphere)
- [ ] Test plan reviewed and approved by MCO team

## 16. Suspension Criteria and Resumption Requirements

### Suspension
- CI infrastructure outage preventing test execution
- Blocking dependency (AGENT-1468, MCO-2214) not available
- P0 bug requiring architectural changes to OSImageStream subsystem

### Resumption
- CI infrastructure restored
- Blocking dependency available and integrated
- P0 bug fixed and verified

## 17. Traceability Matrix

| Jira Issue | Test IDs | Coverage Area |
|------------|----------|---------------|
| MCO-2161 | All TP3-* | Phase 3 epic scope |
| MCO-1995 | TP3-DISC-*, TP3-MCP-*, TP3-XSTREAM-*, TP3-CONFLICT-* | Phase 2 foundation (regression) |
| AGENT-1468 | TP3-AGENT-01 through TP3-AGENT-05 | Agent/installer integration |
| MCO-2214 | TP3-AGENT-03 | ZTP manifest integration |
| OCPSTRAT-1150 | TP3-GA-01, TP3-GA-02 | Strategic dual-stream GA |

## 18. Approvals

| Role | Name | Date | Signature |
|------|------|------|-----------|
| Test Plan Author | TBD | TBD | |
| MCO Tech Lead | TBD | TBD | |
| QE Lead | TBD | TBD | |
