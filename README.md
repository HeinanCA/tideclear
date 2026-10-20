# tideclear

Know your EKS cluster is safe to upgrade.

EKS upgrades one Kubernetes minor version at a time, and each version removes APIs that
workloads may still use. tideclear tells you which clusters are close to the end of support
and which objects in a cluster, or in your manifests, will break on the next version.

It is a single read-only binary. It never changes a cluster, needs no server or account, and
sends no telemetry.

## Install

```bash
go install github.com/HeinanCA/tideclear/cmd/tideclear@latest
```

Release binaries for Linux, macOS and Windows (amd64 and arm64) are published on the
[releases page](https://github.com/HeinanCA/tideclear/releases) from v0.1.0. A Homebrew
formula will follow.

## 30-second usage

List clusters with their support dates (sample output):

```console
$ tideclear clusters --all-regions
CLUSTER  REGION     VERSION  STANDARD END  EXTENDED END  DAYS LEFT  SUPPORT
demo-a   us-east-1  1.35     2027-03-27    2028-03-27    159        standard
demo-b   eu-west-1  1.31     2025-11-26    2026-11-26    38         EXTENDED
demo-c   eu-west-1  1.25     2024-05-01    2025-05-01    -536       UNSUPPORTED
```

`DAYS LEFT` counts to the end of the support phase the cluster is in now. Use `--region` for
one region, or `--cluster NAME` for one cluster.

Scan the cluster in your current kube context against its next minor version (sample output):

```console
$ tideclear scan
cluster: demo-ctx (api.example.test)
KIND            APIVERSION                            NAMESPACE  NAME  REPLACEMENT                      REMOVED IN        SEVERITY
FlowSchema      flowcontrol.apiserver.k8s.io/v1beta3  -          web   flowcontrol.apiserver.k8s.io/v1  1.32              BLOCKER
Ingress         extensions/v1beta1                    demo       web   networking.k8s.io/v1             1.22              BLOCKER
LeaseCandidate  coordination.k8s.io/v1beta1           demo       api   -                                1.39 (projected)  WARNING
```

Scan manifests in a repo or in CI, with no cluster or AWS access:

```console
$ tideclear scan --path ./manifests --target 1.37
path: ./manifests
files scanned: 10
KIND                      APIVERSION                    NAMESPACE  NAME                  REPLACEMENT              REMOVED IN        SEVERITY  ORIGIN
CustomResourceDefinition  apiextensions.k8s.io/v1beta1  -          widgets.example.test  apiextensions.k8s.io/v1  1.22              BLOCKER   crd/crd.yaml:0
IPAddress                 networking.k8s.io/v1beta1     -          api                   networking.k8s.io/v1     1.37              BLOCKER   multidoc/stack.yaml:4
ClusterTrustBundle        certificates.k8s.io/v1beta1   -          api                   certificates.k8s.io/v1   1.40 (projected)  WARNING   bad/nested/bundle.yaml:0
```

Show the EKS Upgrade Insights for one cluster (sample output):

```console
$ tideclear insights --cluster demo-cluster
NAME                                         TARGET  STATUS   REFRESHED
Deprecated APIs removed in Kubernetes v1.32  1.32    ERROR    14h ago
EKS add-on version compatibility             1.32    PASSING  14h ago
Kubelet version skew                         1.32    UNKNOWN  -
```

Reconcile those insights with a live scan. `--insights` needs `--cluster` and a live scan
(sample output):

```console
$ tideclear scan --cluster demo-cluster --insights
cluster: demo-ctx (api.example.test) [EKS: demo-cluster]
KIND        APIVERSION                            NAMESPACE  NAME  REPLACEMENT                      REMOVED IN  SEVERITY
FlowSchema  flowcontrol.apiserver.k8s.io/v1beta3  -          web   flowcontrol.apiserver.k8s.io/v1  1.32        BLOCKER

upgrade insights:
APIVERSION                            KIND                        AWS STATUS  VERDICT                   EVIDENCE
flowcontrol.apiserver.k8s.io/v1beta3  FlowSchema                  ERROR       BLOCKER                   object FlowSchema web
flowcontrol.apiserver.k8s.io/v1beta3  PriorityLevelConfiguration  WARNING     STALE, clears 2026-11-09  client kubectl/1.28 x3, last 2026-10-10
result not clean: 1 stale
```

The `FlowSchema` row is a real blocker: a live object still uses the removed API. The
`PriorityLevelConfiguration` row is STALE: no live object uses it, and the last client call
(2026-10-10) was more than 7 days before the insight refreshed. AWS still reports it, and
clears it on the date shown. Add `--strict` to exit 1 on STALE and UNKNOWN rows too.

Add `--output json` to any command for machine-readable output.

### Commands and flags

| Command | Flags |
|---|---|
| `clusters` | `--profile`, `--region`, `--all-regions`, `--cluster`, `--output text\|json` |
| `insights` | `--cluster` (required), `--profile`, `--region`, `--target`, `--output` |
| `scan` (live) | `--kubeconfig`, `--context`, `--target`, `--cluster`, `--profile`, `--region`, `--insights`, `--strict`, `--output` |
| `scan --path` | `--path`, and `--target` or `--cluster`, `--output` |

The default target is the current minor version plus one. A target more than one minor ahead
is rejected, because EKS upgrades one minor at a time. With `--cluster`, the current version
comes from EKS; otherwise it comes from the API server. `AWS_PROFILE`, `AWS_REGION` and
`KUBECONFIG` are honored.

`insights` lists the `UPGRADE_READINESS` insights of one cluster: name, target version, status
and how long ago EKS refreshed it. `--target` limits it to one version; without it, all
versions are listed. If any insight was refreshed more than 24 hours ago, a hint points you to
the console "Refresh insights" button. `scan --insights` prints the same hint under its insights
table. tideclear never calls `StartInsightsRefresh`, because it
is a write call.

`scan --insights` reads the insights for the target version and reconciles them with the live
scan. It needs `--cluster` and a live scan; it is an error with `--path`. `--strict` makes STALE
and UNKNOWN rows exit 1 (see [Exit codes](#exit-codes)).

### Insight verdicts

AWS builds its findings from a rolling 30-day audit window, so a deprecated API stays flagged
for up to 30 days after you fix it. AWS confirmed this is intentional
([aws/containers-roadmap issue 2569](https://github.com/aws/containers-roadmap/issues/2569)).
`scan --insights` shows one row per deprecated API version in the target's deprecated-API
insight, with a verdict:

| Verdict | Meaning |
|---|---|
| `BLOCKER` | Live objects use the API, or a client called it within 7 days of the insight's refresh time. The row names the objects, or the client's user agent, request count and last call. |
| `STALE` | No live objects, the last client call is older than 7 days, and the live scan read the resource. AWS still flags it. The row shows `clears` with the last call plus 30 days. |
| `OK` | AWS reports `PASSING`, and the data is fresh. |
| `UNKNOWN` | tideclear cannot prove the row clean or stale. See below. |

A row is `UNKNOWN` when:

- the insight status is `UNKNOWN`, or the insights call failed or was denied;
- the insight has no API details;
- the insight data is older than 7 days, has no refresh time, or has a refresh time in the future;
- the live scan did not read the resource, or some of its objects had no apiVersion evidence;
- no client call date is available;
- the insight's `usage` path is not understood;
- AWS returned no deprecated-API insight for the target.

An `UNKNOWN` row is never treated as clean and never called a stale ghost. See
[docs/detection.md](docs/detection.md#reconciling-upgrade-insights) for the order of the rules.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | No blockers. Warnings, STALE and UNKNOWN rows, and skipped checks may be present. |
| 1 | At least one blocker: an object uses an API removed at or before the target, a client still calls a removed API, or an Amazon Linux 2 node cannot follow the control plane. With `--strict`, also a STALE or UNKNOWN insight row, or an UNKNOWN add-on. |
| 2 | Tool, credential or connection error. One line on stderr. |

An incompatible add-on is a `WARNING`. A skipped add-on, nodegroup or node check never makes
`--strict` exit 1. If insights cannot be read, the insights check is skipped and leaves an UNKNOWN
insight row, which `--strict` counts. Expired or invalid credentials, including an expired SSO
session, exit 2. With `--strict`, a line on stderr says how many STALE or UNKNOWN items turned
the exit into 1.

`clusters` exits 0 on success even when a cluster is on extended support; the state is shown,
not treated as an error.

## Add-ons and nodes

`scan --cluster` checks the add-ons and managed nodegroups of the cluster. Every live scan also
checks the nodes through the Kubernetes API.

**Add-ons.** For each add-on that EKS manages, tideclear reads the installed version and asks EKS
which versions support the target. A version that is not on that list is `INCOMPATIBLE`
(a `WARNING`). `MIN COMPATIBLE` is the lowest compatible version that is newer than the installed
one. An add-on with no compatible version listed is `UNKNOWN`. With `--insights`, a
disagreement with the insight's add-on data is noted in the row; the direct check wins.
Self-managed add-ons are not checked.

```console
add-ons:
NAME        CURRENT            MIN COMPATIBLE     STATUS                  NOTE
demo-dns    v1.1.0-eksbuild.1  -                  OK                      -
demo-net    v1.0.0-eksbuild.1  v1.2.0-eksbuild.1  INCOMPATIBLE (WARNING)  insight lists this version as compatible; direct check says not
demo-agent  v3.0.0             -                  UNKNOWN                 no version of this add-on lists Kubernetes 1.35 as compatible
```

**Nodes.** Only findings get a row. tideclear flags:

- Amazon Linux 2 (cgroup v1). Kubelet 1.35 and later does not start on it by default, and EKS
  publishes no AL2 AMIs after 1.32. It is a `BLOCKER` when the target is 1.35 or later, and a
  `WARNING` below that.
- A kubelet newer than the control plane, or more than three minors older.
- A version that cannot be parsed (`WARNING`).

Managed nodegroups are judged by `amiType` (prefix `AL2_`). Other nodes, Karpenter and
self-managed included, are judged by `osImage` (`^Amazon Linux 2( |$)`, so Amazon Linux 2023 does
not match). Fargate nodes are skipped. Nodes of a managed nodegroup are covered by their
nodegroup row, unless the group uses a `CUSTOM` AMI or reports no `amiType`; those nodes are
judged by `osImage`. The AMI prefixes, patterns and version cutoffs, with their sources, are in
`internal/rules/data/nodes.yaml`. See [docs/rules.md](docs/rules.md).

## JSON output

All JSON output has `"schemaVersion": 1`. Fields may be added within version 1; none will be
renamed or removed.

`scan` JSON:

| Field | Meaning |
|---|---|
| `target` | Target version, for example `"1.37"`. |
| `cluster` | `{context, server}` for a live scan. |
| `eksCluster` | Cluster name when `--cluster` was given. |
| `path`, `filesScanned` | For `--path` scans. |
| `findings[]` | `kind`, `apiVersion`, `namespace`, `name`, `replacement`, `removedIn`, `deprecatedIn`, `severity` (`BLOCKER` or `WARNING`), `source` (`pluto` or `k8s-lifecycle`), `projected`, `origin`. |
| `skipped[]` | `group`, `kind`, `reason` for anything that could not be read. In `--path` scans, `group` holds the file path. |
| `unverified` | Number of objects with no apiVersion evidence, which were not checked. |
| `insights[]` | One row per deprecated API version. Present when `--insights` ran, even if empty. |
| `addons[]` | One row per add-on EKS manages. Present when the add-on check ran, even if empty. |
| `nodes[]` | Node and nodegroup findings. Omitted when empty. |
| `checksSkipped[]` | Checks that did not run. Omitted when empty. |

`schemaVersion` stays 1. The four keys above are additive.

- `insights[]` row: `usage`, `apiVersion`, `kind`, `resource`, `awsStatus`, `verdict` (`BLOCKER`,
  `STALE`, `OK` or `UNKNOWN`), `clearsOn` (RFC 3339, null unless `STALE`), `reason`,
  `lastRefreshTime` (null when EKS gave none), `objects[]` (`apiVersion`, `kind`, `namespace`,
  `name`, `origin`) and `clients[]` (`userAgent`, `requests`, `lastRequestTime`).
- `addons[]` row: `name`, `current`, `minCompatible`, `status` (`OK`, `INCOMPATIBLE` or
  `UNKNOWN`), `severity`, `reason`, `insightNote`.
- `nodes[]` row: `source` (`nodegroup` or `node`), `name`, `version`, `amiType`,
  `releaseVersion`, `osImage`, `severity`, `reasons[]`.
- `checksSkipped[]` row: `check` (`insights`, `addons`, `nodegroups` or `nodes`), `reason`.

`insights` JSON has `insights[]` (`id`, `name`, `target`, `status`, `reason`,
`lastRefreshTime`).

`clusters` JSON has `clusters[]` (`name`, `region`, `version`, `standardSupportEnd`,
`extendedSupportEnd`, `daysLeft`, `support`, `onExtended`) and `skippedRegions[]` (`region`,
`error`). `support` is `standard`, `extended`, `unsupported` or `unknown`; `daysLeft` is null when
unknown.

## Required permissions

AWS (only for `clusters`, `insights` and `scan --cluster`). Every action is read-only:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "eks:ListClusters",
        "eks:DescribeCluster",
        "eks:DescribeClusterVersions",
        "eks:ListInsights",
        "eks:DescribeInsight",
        "eks:ListAddons",
        "eks:DescribeAddon",
        "eks:DescribeAddonVersions",
        "eks:ListNodegroups",
        "eks:DescribeNodegroup"
      ],
      "Resource": "*"
    }
  ]
}
```

Kubernetes (only for a live `scan`): `get` and `list` on the resources being checked, plus API
discovery. A kind that returns 403 is skipped and reported, not fatal. The node check also needs
`list` on `nodes`, which is cluster-scoped. The built-in `view` ClusterRole does not grant it.

If a call is denied, the check degrades. A denied `ListInsights`, `DescribeInsight`,
`ListAddons`, `ListNodegroups`, `DescribeNodegroup` or node list skips that check: tideclear
prints one line on stderr that names the check, lists it in `checksSkipped[]`, and still prints
the rest of the report. A denied or failed `DescribeAddon` or `DescribeAddonVersions` marks that
one add-on `UNKNOWN`, with the reason in the `NOTE` column, and the check still runs. A
Kubernetes 401 on the node list exits 2. The `insights` command is the exception: any AWS error
there exits 2. Without `--cluster`, the add-on and nodegroup checks are skipped, and the report
says so.

## How detection works

- **Rules.** tideclear embeds two rule sets:
  - Pluto's deprecation list.
  - A list generated from the lifecycle metadata in `k8s.io/api` v0.37.1, which adds beta API
    removals up to Kubernetes 1.40.

  See [docs/rules.md](docs/rules.md).
- **Live scan.** The API server returns every object through any served version, so listing
  through an old version proves nothing. tideclear lists each covered kind and reports an object
  only when its `kubectl.kubernetes.io/last-applied-configuration` annotation, or its newest
  `managedFields` entry, shows a deprecated apiVersion. See [docs/detection.md](docs/detection.md).
- **Manifest scan.** Each document's own `apiVersion` is checked. `List` kinds and top-level
  sequences are expanded.
- **Severity.**
  - Removed at or before the target: `BLOCKER`.
  - Deprecated but still served: `WARNING`.
  - Removal dates after Kubernetes 1.37 are projected from the deprecation policy, not
    announced, so they are reported as `WARNING` with a `projected` label.

## Limits

- An object written before Kubernetes 1.18, or by a client that leaves no `managedFields` and no
  last-applied annotation, has no evidence. Such objects are counted as `unverified`, not
  checked.
- Helm release history and unrendered chart templates (`{{ }}`) are not scanned. Templates are
  skipped with a warning; render them with `helm template` first.
- `--path` reads `.yaml`, `.yml` and `.json` files. Symlinked subdirectories are not followed.
  Every file it cannot read is listed as skipped.
- With `--path` and `--target`, there is no current version to check the target against.
- `--cluster` and `--context` are independent. They must name the same cluster.
- Rules cover core Kubernetes APIs. Custom resources of third-party controllers are not checked.
- The exec credential plugin (`aws eks get-token`) can print its own lines on stderr. It is
  bounded only by the 10-minute overall scan timeout.
- `--all-regions` uses a built-in region list.
- tideclear assumes an insight's `usage` path has the form `/apis/<group>/<version>/<resource>`
  (or `/api/v1/<resource>`). A path in any other form gives an `UNKNOWN` row.
- AWS audits a 30-day window and refreshes insights about every 24 hours. A deprecated API that
  a controller calls in a loop stays a `BLOCKER` and never turns STALE.
- Each extra check (insights, add-ons, nodegroups, nodes) has its own 20-second budget.
- The Amazon Linux 2 rule is about nodes that must follow the control plane to the target. It
  does not say the nodes fail today.
- The insights, add-on and node checks are tested against fakes. Validation on a real cluster
  is still pending.
- The Pluto rule data was last updated in March 2026. Rules are refreshed by updating the pinned
  copy (see [docs/rules.md](docs/rules.md)).

## Roadmap

- Done in weekend 2: EKS Upgrade Insights with reconciliation against the 30-day audit window,
  add-on compatibility, and node AMI and kubelet checks.
- Karpenter and VPC CNI compatibility checks.

## Credits

- [Pluto](https://github.com/FairwindsOps/pluto) by Fairwinds (Apache-2.0) provides the embedded
  deprecation list. License and source are in `internal/rules/data/`.
- [kube-no-trouble (kubent)](https://github.com/doitintl/kube-no-trouble) by DoiT (MIT) was the
  reference for detecting the applied apiVersion from the last-applied annotation.
- The Kubernetes Authors' `k8s.io/api` (Apache-2.0) provides the lifecycle metadata.

## License

MIT. See [LICENSE](LICENSE).
