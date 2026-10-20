# Rules

tideclear decides what to report from two embedded rule files in `internal/rules/data/`. Both
use the same schema: `version` (apiVersion), `kind`, `deprecated-in`, `removed-in`,
`replacement-api`, `replacement-available-in` and `component`.

## Sources

| File | Source | What it adds |
|---|---|---|
| `versions.yaml` | [Pluto](https://github.com/FairwindsOps/pluto) at commit `9495152d614581a988e31414fa64e8b82474d956`, unmodified (Apache-2.0) | Curated deprecations back to Kubernetes 1.16, including `apiextensions.k8s.io/v1beta1` CustomResourceDefinition. |
| `k8s-lifecycle.yaml` | Generated from `k8s.io/api` v0.37.1 (Kubernetes 1.37) | Every beta API type's deprecated and removed release, as the Kubernetes source records them. Covers removals through 1.40. |

`NOTICE` records the Pluto commit, fetch date and checksum, and the `k8s.io/api` version.
`LICENSE-pluto` is Pluto's license.

Only rules with `component: k8s` are matched. Pluto's cert-manager and istio entries are kept in
the file but not used.

## Merging

Rules are keyed by apiVersion and kind. When both files have a rule for the same key, the
`k8s-lifecycle` rule wins. For example, Pluto lists `resource.k8s.io/v1beta1` as removed in
1.36, while Kubernetes 1.37 still serves it and records removal in 1.38.

When the Kubernetes source names no replacement, the generator uses the stable version of the
same group and kind if one exists, for example `resource.k8s.io/v1`. If none exists, the
replacement is shown as `-`.

## Severity

| Condition | Severity |
|---|---|
| Removed at or before the target, in a release up to 1.37 | `BLOCKER` |
| Removed at or before the target, in a release after 1.37 | `WARNING`, labelled `projected` |
| Deprecated at or before the target, not yet removed | `WARNING` |

Removals after 1.37 are not announcements. Most beta types in `k8s.io/api` carry only an
`introduced` tag. The deprecated and removed releases are then filled in from the deprecation
policy's default timeline (three minors each), and the Kubernetes project can extend it.

## Node and AMI rules

`scan` also checks worker nodes. The data lives in `internal/rules/data/nodes.yaml`
(embedded, strictly decoded: an unknown key is an error), not in the checks.

| Node | Rule |
|---|---|
| Managed nodegroup with an `AL2_` amiType (not `AL2023_`) | Amazon Linux 2 |
| Any node whose `osImage` matches `^Amazon Linux 2( \|$)` ("Amazon Linux 2023" does not) | Amazon Linux 2 |
| Amazon Linux 2, target 1.35 or later | `BLOCKER`: the kubelet's `failCgroupV1` defaults to true from 1.35 and AL2 is cgroup v1 |
| Amazon Linux 2, target below 1.35 | `WARNING` |
| Kubelet newer than the control plane, or more than 3 minors older (now, or after the upgrade) | `WARNING` |
| Kubelet or nodegroup version that cannot be parsed | `WARNING`, "version unknown" |

A nodegroup's `releaseVersion` is shown but never parsed (it can be an AMI ID or an OS
release). Fargate nodes are skipped. Nodes of a managed nodegroup are judged through the
nodegroup row when the nodegroup check ran, except `CUSTOM` groups, whose nodes are judged
by `osImage`. Only findings get a row.

Sources (fetched 2026-10-20), also recorded in `nodes.yaml`:

- <https://docs.aws.amazon.com/eks/latest/userguide/eks-ami-deprecation-faqs.html>: Kubernetes 1.32 is the last version with EKS AL2 AMIs.
- <https://kubernetes.io/blog/2026/10/06/kubernetes-cgroups-v2-shift/> and <https://kubernetes.dev/resources/keps/5573>: `failCgroupV1` defaults to true from 1.35.
- <https://kubernetes.io/releases/version-skew-policy/>: kubelet may be up to three minors older than kube-apiserver, never newer.

## Refreshing the data

Pluto data:

```bash
SHA=<new pluto commit>
curl -fsSLo internal/rules/data/versions.yaml \
  "https://raw.githubusercontent.com/FairwindsOps/pluto/$SHA/versions.yaml"
curl -fsSLo internal/rules/data/LICENSE-pluto \
  "https://raw.githubusercontent.com/FairwindsOps/pluto/$SHA/LICENSE"
shasum -a 256 internal/rules/data/versions.yaml   # record SHA and checksum in NOTICE
```

Kubernetes lifecycle data:

```bash
go get k8s.io/api@<new version>
go generate ./internal/rules/...
```

Then update the version in `NOTICE` and run `make test`.
