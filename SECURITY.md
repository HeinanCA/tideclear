# Security Policy

## Reporting a vulnerability

Please do not open a public issue for a security problem.

Report it privately through GitHub:
[Report a vulnerability](https://github.com/HeinanCA/tideclear/security/advisories/new)
(Security tab → Report a vulnerability).

Include the tideclear version (`tideclear --version`), what you ran, and what you expected
versus what happened. A minimal reproduction helps.

## Supported versions

tideclear is pre-1.0. Only the latest release is supported. Fixes land in a new release; they
are not backported.

## What to expect

tideclear is maintained by one person, so there is no SLA. The aim is to acknowledge a report
within 7 days and to say whether it is accepted or declined. Fix timing depends on severity and
the maintainer's availability.

## Scope

tideclear is a read-only CLI. It sends only `get` and `list` calls to a Kubernetes cluster and
no write calls to AWS.

In scope:

- The `tideclear` binary and its source.
- The rule data under `internal/rules/data`.
- Release artifacts: the archives and `checksums.txt` published on GitHub Releases.
- Any way to make tideclear write to, or change, an AWS account or a cluster.

Out of scope:

- Your AWS account, your clusters and their configuration. tideclear reports on them; securing
  them is up to you.
- Vulnerabilities in dependencies with no effect on tideclear. Report those upstream.
