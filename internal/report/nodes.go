package report

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/HeinanCA/tideclear/internal/nodes"
)

// nodesSection prints the node findings table; silent with no rows.
func nodesSection(w io.Writer, rows []nodes.Row) error {
	if len(rows) == 0 {
		return nil
	}
	if _, err := fmt.Fprint(w, "\nnodes:\n"); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SOURCE\tNAME\tVERSION\tAMI\tRELEASE\tOS IMAGE\tSEVERITY\tREASONS")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Source, plain(r.Name), dash(r.Version), dash(r.AMIType),
			dash(plain(r.ReleaseVersion)), dash(plain(r.OSImage)), r.Severity, strings.Join(r.Reasons, "; "))
	}
	return tw.Flush()
}

type nodeRowJSON struct {
	Source         string   `json:"source"`
	Name           string   `json:"name"`
	Version        string   `json:"version"`
	AMIType        string   `json:"amiType"`
	ReleaseVersion string   `json:"releaseVersion"`
	OSImage        string   `json:"osImage"`
	Severity       string   `json:"severity"`
	Reasons        []string `json:"reasons"`
}

func nodeJSON(r nodes.Row) nodeRowJSON {
	reasons := r.Reasons
	if reasons == nil {
		reasons = []string{}
	}
	return nodeRowJSON{Source: r.Source, Name: r.Name, Version: r.Version, AMIType: r.AMIType,
		ReleaseVersion: r.ReleaseVersion, OSImage: r.OSImage, Severity: string(r.Severity), Reasons: reasons}
}
