package report

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/HeinanCA/tideclear/internal/addons"
)

// addonsSection prints the add-on table. With no rows it is silent unless
// the check ran, when it says there was nothing to check.
func addonsSection(w io.Writer, ran bool, rows []addons.Row) error {
	if len(rows) == 0 {
		if !ran {
			return nil
		}
		_, err := fmt.Fprintln(w, "add-ons: none managed by EKS (self-managed add-ons are not checked)")
		return err
	}
	if _, err := fmt.Fprintln(w, "add-ons:"); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tCURRENT\tMIN COMPATIBLE\tSTATUS\tNOTE")
	for _, r := range rows {
		status := string(r.Status)
		if r.Severity != "" {
			status += " (" + string(r.Severity) + ")"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, dash(r.Current), dash(r.MinCompatible), status, dash(addonNote(r)))
	}
	return tw.Flush()
}

func addonNote(r addons.Row) string {
	var parts []string
	for _, p := range []string{r.Reason, r.InsightNote} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "; ")
}

type addonRowJSON struct {
	Name          string `json:"name"`
	Current       string `json:"current"`
	MinCompatible string `json:"minCompatible"`
	Status        string `json:"status"`
	Severity      string `json:"severity"`
	Reason        string `json:"reason"`
	InsightNote   string `json:"insightNote"`
}

func addonJSON(r addons.Row) addonRowJSON {
	return addonRowJSON{Name: r.Name, Current: r.Current, MinCompatible: r.MinCompatible, Status: string(r.Status),
		Severity: string(r.Severity), Reason: r.Reason, InsightNote: r.InsightNote}
}
