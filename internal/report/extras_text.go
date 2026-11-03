package report

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/reconcile"
)

const maxEvidence = 3

// extrasText writes the sections the optional checks add; nothing for zero Extras.
func extrasText(w io.Writer, ex Extras) error {
	if err := missingSection(w, ex.Missing); err != nil {
		return err
	}
	if ex.InsightsRan {
		if err := insightsSection(w, ex.Insights); err != nil {
			return err
		}
		if ex.RefreshStale {
			if _, err := fmt.Fprintln(w, refreshHint); err != nil {
				return err
			}
		}
	}
	if err := addonsSection(w, ex.AddonsRan, ex.Addons); err != nil {
		return err
	}
	if err := nodesSection(w, ex.Nodes); err != nil {
		return err
	}
	for _, s := range ex.Skipped {
		if _, err := fmt.Fprintf(w, "check skipped: %s: %s\n", s.Check, s.Reason); err != nil {
			return err
		}
	}
	return footer(w, ex.verdicts(reconcile.VerdictStale), ex.unknowns(), len(ex.Skipped))
}

// footer is the closing line: how much of the result is unproven.
func footer(w io.Writer, stale, unknown, skipped int) error {
	var parts []string
	if stale > 0 {
		parts = append(parts, fmt.Sprintf("%d stale", stale))
	}
	if unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d unknown", unknown))
	}
	if skipped > 0 {
		parts = append(parts, Plural(skipped, "check skipped", "checks skipped"))
	}
	if len(parts) == 0 {
		return nil
	}
	_, err := fmt.Fprintf(w, "result not clean: %s\n", strings.Join(parts, ", "))
	return err
}

func insightsSection(w io.Writer, rows []reconcile.Row) error {
	if len(rows) == 0 {
		_, err := fmt.Fprint(w, "\nupgrade insights: no deprecated API usage reported\n")
		return err
	}
	if _, err := fmt.Fprint(w, "\nupgrade insights:\n"); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "APIVERSION\tKIND\tAWS STATUS\tVERDICT\tEVIDENCE")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", dash(r.APIVersion), dash(r.Kind), dash(r.AWSStatus), verdictText(r), evidenceText(r))
	}
	return tw.Flush()
}

func verdictText(r reconcile.Row) string {
	if r.Verdict == reconcile.VerdictStale && r.ClearsOn != nil {
		return "STALE, clears " + r.ClearsOn.UTC().Format("2006-01-02")
	}
	return string(r.Verdict)
}

// evidenceText lists live objects and calling clients; with neither, the reason.
func evidenceText(r reconcile.Row) string {
	var parts []string
	for i, o := range r.Objects {
		if i == maxEvidence {
			parts = append(parts, fmt.Sprintf("+%d more objects", len(r.Objects)-maxEvidence))
			break
		}
		parts = append(parts, "object "+objectName(o.Kind, o.Namespace, o.Name))
	}
	for i, c := range r.Clients {
		if i == maxEvidence {
			parts = append(parts, fmt.Sprintf("+%d more clients", len(r.Clients)-maxEvidence))
			break
		}
		last := "no time"
		if c.LastRequest != nil {
			last = c.LastRequest.UTC().Format("2006-01-02")
		}
		parts = append(parts, fmt.Sprintf("client %s x%d, last %s", plain(c.UserAgent), c.Requests, last))
	}
	if len(parts) == 0 {
		return dash(r.Reason)
	}
	if r.Verdict == reconcile.VerdictUnknown && r.Reason != "" {
		parts = append(parts, r.Reason)
	}
	return strings.Join(parts, "; ")
}

func objectName(kind, ns, name string) string {
	if ns != "" {
		name = ns + "/" + name
	}
	return kind + " " + name
}

// missingSection lists the denied list permissions and a ClusterRole that
// grants them, rules grouped by apiGroup in the (sorted) input order. It prints
// nothing when nothing is missing.
func missingSection(w io.Writer, missing []apiscan.Permission) error {
	if len(missing) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("\nmissing permissions (the scan could not read these, so its results are partial):\n")
	var groups []string
	byGroup := map[string][]string{}
	for _, p := range missing {
		fmt.Fprintf(&b, "  %s\n", p)
		if _, seen := byGroup[p.Group]; !seen {
			groups = append(groups, p.Group)
		}
		byGroup[p.Group] = append(byGroup[p.Group], p.Resource)
	}
	b.WriteString("\nsuggested ClusterRole:\napiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: tideclear-reader\nrules:\n")
	for _, g := range groups {
		fmt.Fprintf(&b, "- apiGroups: [%q]\n  resources: [\"%s\"]\n  verbs: [\"list\"]\n", g, strings.Join(byGroup[g], `", "`))
	}
	_, err := io.WriteString(w, b.String())
	return err
}
