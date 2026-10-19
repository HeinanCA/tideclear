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

Add `--output json` to any command for machine-readable output.

### Commands and flags

| Command | Flags |
|---|---|
| `clusters` | `--profile`, `--region`, `--all-regions`, `--cluster`, `--output text\|json` |
| `scan` (live) | `--kubeconfig`, `--context`, `--target`, `--cluster`, `--profile`, `--region`, `--output` |
| `scan --path` | `--path`, and `--target` or `--cluster`, `--output` |

The default target is the current minor version plus one. A target more than one minor ahead
is rejected, because EKS upgrades one minor at a time. With `--cluster`, the current version
comes from EKS; otherwise it comes from the API server. `AWS_PROFILE`, `AWS_REGION` and
`KUBECONFIG` are honored.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | No blockers. Warnings and skipped items may be present. |
| 1 | At least one blocker: an object uses an API removed at or before the target. |
| 2 | Tool, credential or connection error. One line on stderr. |

`clusters` exits 0 on success even when a cluster is on extended support; the state is shown,
not treated as an error.

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

`clusters` JSON has `clusters[]` (`name`, `region`, `version`, `standardSupportEnd`,
`extendedSupportEnd`, `daysLeft`, `support`, `onExtended`) and `skippedRegions[]` (`region`,
`error`). `support` is `standard`, `extended`, `unsupported` or `unknown`; `daysLeft` is null when
unknown.

## Required permissions

AWS (only for `clusters` and `scan --cluster`):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["eks:ListClusters", "eks:DescribeCluster", "eks:DescribeClusterVersions"],
      "Resource": "*"
    }
  ]
}
```

Kubernetes (only for a live `scan`): `get` and `list` on the resources being checked, plus API
discovery. A kind that returns 403 is skipped and reported, not fatal.

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
- The Pluto rule data was last updated in March 2026. Rules are refreshed by updating the pinned
  copy (see [docs/rules.md](docs/rules.md)).

## Roadmap

- EKS Upgrade Insights and reconciliation with the 30-day audit window.
- Add-on, Karpenter, VPC CNI and node AMI compatibility checks.

## Credits

- [Pluto](https://github.com/FairwindsOps/pluto) by Fairwinds (Apache-2.0) provides the embedded
  deprecation list. License and source are in `internal/rules/data/`.
- [kube-no-trouble (kubent)](https://github.com/doitintl/kube-no-trouble) by DoiT (MIT) was the
  reference for detecting the applied apiVersion from the last-applied annotation.
- The Kubernetes Authors' `k8s.io/api` (Apache-2.0) provides the lifecycle metadata.

## License

MIT. See [LICENSE](LICENSE).
