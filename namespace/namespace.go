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
	NamespaceDir     = "ns"
	RefHead          = "refs/head"
)

var ErrInvalidName = errors.New("namespace: invalid name")

func validate(name string) error {
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

func ValidateTenant(tenant string) error {
	return validate(tenant)
}

func ValidateNamespace(ns string) error {
	return validate(ns)
}

func ValidateBranch(branch string) error {
	return validate(branch)
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

func SegmentKey(tenant, ns, branch, segID string) string {
	if tenant == "" {
		tenant = DefaultTenant
	}
	if ns == "" {
		ns = DefaultNamespace
	}
	if branch == "" {
		branch = DefaultBranch
	}
	return path.Join(tenant, NamespaceDir, ns, "segments", branch, segID+".recordio")
}

type Scope struct {
	Tenant    string
	Namespace string
}

func NewScope(tenant, ns string) (Scope, error) {
	s := Scope{Tenant: tenant, Namespace: ns}
	if err := s.Validate(); err != nil {
		return Scope{}, err
	}
	return s, nil
}

func (s Scope) Validate() error {
	if s.Tenant != "" {
		if err := ValidateTenant(s.Tenant); err != nil {
			return err
		}
	}
	if s.Namespace != "" {
		if err := ValidateNamespace(s.Namespace); err != nil {
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

func (s Scope) CatalogPath() string {
	return CatalogPath(s.Tenant)
}

func CatalogPath(tenant string) string {
	if tenant == "" {
		tenant = DefaultTenant
	}
	return path.Join(tenant, "ns.json")
}
