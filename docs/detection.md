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
