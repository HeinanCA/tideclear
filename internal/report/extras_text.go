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
	}
	if err := addonsSection(w, ex.Addons); err != nil {
		return err
	}
	if n := ex.unknowns(); n > 0 {
		if _, err := fmt.Fprintf(w, "result not clean: %d unknown\n", n); err != nil {
			return err
		}
	}
	for _, s := range ex.Skipped {
		if _, err := fmt.Fprintf(w, "check skipped: %s: %s\n", s.Check, s.Reason); err != nil {
			return err
		}
	}
	return nil
}

func insightsSection(w io.Writer, rows []reconcile.Row) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "upgrade insights: no deprecated API usage reported")
		return err
	}
	if _, err := fmt.Fprintln(w, "upgrade insights:"); err != nil {
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
		parts = append(parts, fmt.Sprintf("client %s x%d, last %s", c.UserAgent, c.Requests, last))
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
