package report

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/HeinanCA/tideclear/internal/reconcile"
)

const maxEvidence = 3

// extrasText writes the sections the optional checks add; nothing for zero Extras.
func extrasText(w io.Writer, ex Extras) error {
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
