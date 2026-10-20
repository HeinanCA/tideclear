# How detection works

## Why listing is not enough

The Kubernetes API server stores one copy of each object and converts it on read. Listing
Ingresses through `extensions/v1beta1` returns every Ingress in the cluster, including ones
created with `networking.k8s.io/v1`. A tool that reports everything it can list through an old
version produces false positives.

## Live scan

1. **Discover.** tideclear reads the served API groups. For each group that holds a kind the
   rules cover, it reads the preferred version's resources. It then reads the other served
   versions for any covered kind still not found.
2. **List.** Each covered kind is listed once, across all namespaces, with paging. Only `get`
   and `list` requests are sent.
3. **Find evidence.** For each object, tideclear looks at:
   - the `apiVersion` in the `kubectl.kubernetes.io/last-applied-configuration` annotation, and
   - the newest `managedFields` entry for the main resource, by `time`. Entries for
     subresources such as `status` are ignored, because controllers write them with the current
     version.

   Older `managedFields` entries never trigger a finding. When an object is fixed by applying it
   with the replacement version, the newest entry moves to the new version and the finding goes
   away.
4. **Match.** Each apiVersion found in the evidence is matched against the rules at the target
   version. See [rules.md](rules.md).

Objects with no evidence are counted as `unverified` and are not checked.

### Partial results

A failure in one part of the API does not stop the scan:

- A kind that returns 403 on `list` is skipped.
- A group whose discovery fails is skipped, for example an aggregated API that is down.

Each skip is printed on stderr, listed in the JSON `skipped` array, and noted in the text
footer as partial results. The exit code reflects only what was scanned.

Other errors stop the scan with exit 2. These include failure to reach the API server, failure
of API discovery as a whole, and other list errors.

### Timeouts

Each request to the API server times out after 30 seconds. The whole scan times out after 10
minutes. The first Ctrl-C stops the scan with exit 2, and a second Ctrl-C ends the process at
once.

## Manifest scan (`--path`)

Each document is evidence of its own `apiVersion`.

| Input | Handling |
|---|---|
| Multi-document YAML, JSON | Each document is checked. |
| `kind: List` and other `*List` kinds | Items are expanded. |
| Top-level sequence (YAML list or JSON array) | Items are expanded. |
| Document missing `apiVersion` or `kind` | Counted as `unverified`. |
| Malformed document | Documents before it in the same file are still checked; the rest of the file is skipped. |
| Unreadable file or directory, symlinked subdirectory, FIFO or other non-regular file | Skipped, with the reason. |
| `--path` itself is a symlink | Resolved before the walk. |

`ORIGIN` is `file:document` or `file:document/item`, relative to `--path`.

## Reconciling Upgrade Insights

With `scan --insights --cluster NAME`, tideclear reads the EKS Upgrade Insights for the target
version and compares each deprecated API version in them with the live scan. AWS builds its
findings from a rolling 30-day audit window, so an API stays flagged for up to 30 days after you
fix it (aws/containers-roadmap issue 2569). The live scan shows what is true now.

Each deprecation detail gets one row. The first rule that matches decides:

1. Live objects use this API version: `BLOCKER`.
2. Insight status `UNKNOWN`: `UNKNOWN`.
3. Insight status `PASSING`: `OK`, or `UNKNOWN` if the data fails the freshness gate.
4. A client called the API within 7 days of the insight's refresh time, or has no call time:
   `BLOCKER`.
5. Otherwise, `STALE` when all of these hold: the live scan read the resource, none of its objects
   lacked apiVersion evidence, and the data passes the freshness gate. It clears on the newest
   client call plus 30 days.
6. Anything else is `UNKNOWN`: no client call date, resource not read live, objects without
   apiVersion evidence, or data that fails the gate.

Other `UNKNOWN` causes: a `usage` path that is not `/apis/<group>/<version>/<resource>` or
`/api/v1/<resource>`, an insight with no API details (unless it is `PASSING` with fresh data),
and no deprecated-API insight for the target.

**Windows run from the refresh time.** Client call times are as old as the insight's last
refresh, so the 7-day window is measured from that time, not from now. A call 6 days before a
refresh that is 3 days old is still active.

**Freshness gate.** `OK` and `STALE` need a refresh time that exists, is not in the future, and
is at most 7 days old. Without that, the call dates cannot be trusted and the row is `UNKNOWN`.

tideclear never calls `StartInsightsRefresh`. The `insights` command and the insights table of
`scan --insights` print a hint to use the console "Refresh insights" button when an insight was
refreshed more than 24 hours ago.

If the insights cannot be read, the rest of the report still prints and the section shows one
`UNKNOWN` row. Expired or invalid credentials exit 2.
