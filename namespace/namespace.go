package namespace

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

const (
	DefaultTenant    = "cloudy"
	DefaultNamespace = "default"
	DefaultBranch    = "main"
	NamespaceDir     = "ns"
	RefHead          = "refs/head"
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

func BranchRef(tenant, ns, branch string) string {
	if tenant == "" {
		tenant = DefaultTenant
	}
	if ns == "" {
		ns = DefaultNamespace
	}
	if branch == "" {
		branch = DefaultBranch
	}
	return path.Join(tenant, NamespaceDir, ns, RefHead, branch)
}

func SegmentKey(tenant, ns, segID string) string {
	if tenant == "" {
		tenant = DefaultTenant
	}
	if ns == "" {
		ns = DefaultNamespace
	}
	return path.Join(tenant, NamespaceDir, ns, "segments", segID+".recordio")
}

func ScopeFromRef(branchRef string) (Scope, string) {
	if prefix, branch, ok := strings.Cut(branchRef, "/"+RefHead+"/"); ok {
		tenant, ns, _ := strings.Cut(prefix, "/"+NamespaceDir+"/")
		return Scope{Tenant: tenant, Namespace: ns}, branch
	}
	if branch, ok := strings.CutPrefix(branchRef, RefHead+"/"); ok {
		return Scope{}, branch
	}
	return Scope{}, branchRef
}

type Scope struct {
	Tenant    string
	Namespace string
}

func (s Scope) Validate() error {
	if s.Tenant != "" {
		if err := ValidateName(s.Tenant); err != nil {
			return err
		}
	}
	if s.Namespace != "" {
		if err := ValidateName(s.Namespace); err != nil {
			return err
		}
	}
	return nil
}

func (s Scope) tenant() string {
	if s.Tenant != "" {
		return s.Tenant
	}
	return DefaultTenant
}

func (s Scope) namespace() string {
	if s.Namespace != "" {
		return s.Namespace
	}
	return DefaultNamespace
}

func (s Scope) Prefix() string {
	return path.Join(s.tenant(), NamespaceDir, s.namespace())
}

func (s Scope) Path(subpath string) string {
	return path.Join(s.Prefix(), subpath)
}

func (s Scope) WALPrefix() string {
	return s.Path("wal")
}

func (s Scope) BranchRef(branch string) string {
	if branch == "" {
		branch = DefaultBranch
	}
	return s.Path(path.Join(RefHead, branch))
}

func (s Scope) SegmentsPrefix() string {
	return s.Path("segments")
}

func (s Scope) SegmentKey(segID string) string {
	return s.Path(path.Join("segments", segID+".recordio"))
}

func (s Scope) BranchesPath() string {
	return s.Path("branches.json")
}

func BranchesPath(tenant, ns string) string {
	if tenant == "" {
		tenant = DefaultTenant
	}
	if ns == "" {
		ns = DefaultNamespace
	}
	return path.Join(tenant, NamespaceDir, ns, "branches.json")
}

func (s Scope) CatalogPath() string {
	return CatalogPath(s.Tenant)
}

func CatalogPath(tenant string) string {
	if tenant == "" {
		tenant = DefaultTenant
	}
	return path.Join(tenant, "ns.json")
}
