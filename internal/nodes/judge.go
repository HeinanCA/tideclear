package nodes

import (
	"fmt"

	"github.com/HeinanCA/tideclear/internal/rules"
)

// judgement accumulates the reasons one node or nodegroup is flagged and the worst severity among them.
type judgement struct {
	severity rules.Severity
	reasons  []string
}

func (j *judgement) found() bool { return len(j.reasons) > 0 }

func (j *judgement) add(sev rules.Severity, reason string) {
	j.reasons = append(j.reasons, reason)
	if j.severity != rules.SeverityBlocker {
		j.severity = sev
	}
}

// al2 flags Amazon Linux 2 (cgroup v1): BLOCKER from the cgroup v2 cutoff, WARNING below.
// The reason is one clause: the report joins reasons with "; ".
func (j *judgement) al2(nr *rules.NodeRules, target rules.Version, what string) {
	sev := nr.AL2Severity(target)
	msg := fmt.Sprintf("Amazon Linux 2 (%s) is cgroup v1: kubelet %s+ will not start on it and EKS AL2 AMIs stop at %s, so these nodes cannot follow the control plane to %s",
		what, nr.CgroupV2Required, nr.LastAL2AMIVersion, target)
	j.add(sev, msg)
}

// skew flags a kubelet version newer than the control plane, or more than
// MaxMinorsOlder behind it now or after the upgrade. An empty or unparseable
// version is a WARNING: never silently clean.
func (j *judgement) skew(kubelet string, current, target rules.Version, nr *rules.NodeRules) {
	kv, err := rules.ParseNodeVersion(kubelet)
	if err != nil {
		j.add(rules.SeverityWarning, "version unknown")
		return
	}
	switch {
	case minorsBehind(current, kv) < 0:
		j.add(rules.SeverityWarning, fmt.Sprintf("kubelet %s is newer than the control plane %s", kv, current))
	case minorsBehind(current, kv) > nr.MaxMinorsOlder:
		j.add(rules.SeverityWarning, fmt.Sprintf("kubelet %s is %d minors behind the control plane %s (max %d)", kv, minorsBehind(current, kv), current, nr.MaxMinorsOlder))
	case minorsBehind(target, kv) > nr.MaxMinorsOlder:
		j.add(rules.SeverityWarning, fmt.Sprintf("kubelet %s would be %d minors behind target %s (max %d)", kv, minorsBehind(target, kv), target, nr.MaxMinorsOlder))
	}
}

// minorsBehind is how many minors kubelet trails cp (negative: newer). A
// different major counts as far out of policy.
func minorsBehind(cp, kubelet rules.Version) int {
	if cp.Major != kubelet.Major {
		return 1 << 20 * (cp.Major - kubelet.Major)
	}
	return cp.Minor - kubelet.Minor
}
