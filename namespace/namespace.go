package namespace

import (
	"errors"
	"fmt"
	"path"
)

const (
	DefaultTenant    = "cloudy"
	DefaultNamespace = "default"
	DefaultBranch    = "main"
	CatalogFile      = "ns.json"
	namespaceDir     = "ns"
	refHeads         = "refs/heads"
)

var ErrInvalidName = errors.New("namespace: invalid name")

func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty name", ErrInvalidName)
	}

	first := name[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')) {
		return fmt.Errorf("%w: must start with letter: %q", ErrInvalidName, name)
	}

	for i := 1; i < len(name); i++ {
		c := name[i]
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		case c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return fmt.Errorf("%w: invalid character %q in %q", ErrInvalidName, c, name)
		}
	}
	return nil
}

type Scope struct {
	Namespace string
}

func (s Scope) Validate() error {
	return ValidateName(s.Namespace)
}

func (s Scope) Prefix() string {
	return path.Join(namespaceDir, s.Namespace)
}

func (s Scope) Path(subpath string) string {
	return path.Join(s.Prefix(), subpath)
}

func (s Scope) WALPrefix() string {
	return s.Path("wal")
}

func (s Scope) ManifestKey(branch string) string {
	return s.Path(path.Join(refHeads, branch+".json"))
}

func (s Scope) SegmentsPrefix() string {
	return s.Path("segments")
}

func (s Scope) SegmentKey(segID string) string {
	return s.Path(path.Join("segments", segID+".recordio"))
}

func (s Scope) branchesKey() string {
	return s.Path("branches.json")
}
