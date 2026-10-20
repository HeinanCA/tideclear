package apiscan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/HeinanCA/tideclear/internal/rules"
)

type manifestSource struct {
	root string
	warn io.Writer
}

// NewManifestSource reads every .yaml/.yml/.json file under root (or root
// itself if it is a file). Each document with an apiVersion and kind is
// evidence of itself, and so is each item of a List; Origin is "path:docIndex"
// (or "path:docIndex/itemIndex") with path relative to root. Anything it cannot
// read is skipped with one line on warn and reported in Observed.Skipped: a
// scan must never look clean for input it did not read.
func NewManifestSource(root string, warn io.Writer) Source {
	return manifestSource{root: root, warn: warn}
}

func (manifestSource) ServerVersion(context.Context) (rules.Version, bool, error) {
	return rules.Version{}, false, nil
}

func (m manifestSource) Objects(ctx context.Context) (Observed, error) {
	var obs Observed
	root, err := filepath.EvalSymlinks(m.root)
	if err != nil {
		return obs, errors.New(oneLine(err.Error()))
	}
	info, err := os.Stat(root)
	if err != nil {
		return obs, errors.New(oneLine(err.Error()))
	}
	if !info.IsDir() {
		return m.scanRootFile(root, info, &obs)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		rel := relTo(root, path)
		if err != nil {
			if path == root {
				return err
			}
			reason := "unreadable: "
			if d != nil && d.IsDir() {
				reason = "unreadable directory: "
			}
			m.skip(&obs, rel, reason+oneLine(err.Error()))
			return nil
		}
		if d.IsDir() {
			return nil
		}
		kind := d.Type()
		if kind&fs.ModeSymlink != 0 {
			st, serr := os.Stat(path)
			switch {
			case serr != nil:
				if isManifest(path) {
					m.skip(&obs, rel, "unreadable: "+oneLine(serr.Error()))
				}
				return nil
			case st.IsDir():
				m.skip(&obs, rel, "symlinked directory not followed")
				return nil
			}
			kind = st.Mode().Type()
		}
		if !isManifest(path) {
			return nil
		}
		if !kind.IsRegular() {
			m.skip(&obs, rel, "not a regular file")
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			m.skip(&obs, rel, "unreadable: "+oneLine(rerr.Error()))
			return nil
		}
		m.ingest(&obs, rel, data)
		return nil
	})
	if err != nil {
		return Observed{}, errors.New(oneLine(err.Error()))
	}
	return obs, nil
}

// scanRootFile handles --path naming one file; every problem with it is fatal.
func (m manifestSource) scanRootFile(path string, info fs.FileInfo, obs *Observed) (Observed, error) {
	switch {
	case !isManifest(m.root):
		return *obs, errors.New("not a YAML or JSON file: " + oneLine(m.root))
	case !info.Mode().IsRegular():
		return *obs, errors.New("not a regular file: " + oneLine(m.root))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return *obs, errors.New("unreadable: " + oneLine(err.Error()))
	}
	m.ingest(obs, filepath.Base(m.root), data)
	return *obs, nil
}

func (m manifestSource) ingest(obs *Observed, rel string, data []byte) {
	obs.FilesScanned++
	objs, unverified, derr := decodeManifest(data, rel)
	obs.Objects = append(obs.Objects, objs...)
	obs.Unverified += unverified
	if derr != nil {
		m.skip(obs, rel, derr.Error())
	}
}

func (m manifestSource) skip(obs *Observed, rel, reason string) {
	_, _ = fmt.Fprintf(m.warn, "tideclear: warning: skipped file %s: %s\n", oneLine(rel), reason)
	obs.Skipped = append(obs.Skipped, Skipped{Group: rel, Reason: reason})
}

func relTo(base, path string) string {
	if rel, err := filepath.Rel(base, path); err == nil {
		return rel
	}
	return path
}

func isManifest(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml", ".json":
		return true
	}
	return false
}

func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' }), "; ")
}
