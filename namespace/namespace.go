package namespace

import (
	"errors"
	"fmt"
	"path"
)

const (
	DefaultTenant = "cloudy"
	refPrefix     = "refs/heads/"
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

func (s Scope) Prefix() string {
	return path.Join(s.Tenant, s.Namespace)
}

func (s Scope) Path(subpath string) string {
	p := s.Prefix()
	if p == "" {
		return subpath
	}
	return path.Join(p, subpath)
}

func (s Scope) WALPrefix() string {
	return s.Path("wal")
}

func (s Scope) BranchRef(branch string) string {
	return s.Path(refPrefix + branch)
}

func (s Scope) SegmentsPrefix() string {
	return s.Path("segments")
}
