package gitref

import (
	"errors"
	"fmt"
	"strings"
)

type Namespace uint8

const (
	NamespaceLocalBranch Namespace = iota + 1
	NamespaceTag
	NamespaceRemote
)

type Reference struct {
	raw            string
	namespace      Namespace
	name           string
	nameComponents []string
}

func Parse(raw string) (Reference, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return Reference{}, errors.New("Git ref must be nonblank and have no surrounding whitespace")
	}
	components := strings.Split(raw, "/")
	if len(components) < 3 || components[0] != "refs" {
		return Reference{}, errors.New("Git ref must contain refs, namespace, and name components")
	}
	namespace, err := parseNamespace(components[1])
	if err != nil {
		return Reference{}, err
	}
	nameComponents := components[2:]
	for _, component := range nameComponents {
		if err := validateComponent(component); err != nil {
			return Reference{}, err
		}
	}
	return Reference{
		raw:            raw,
		namespace:      namespace,
		name:           joinComponents(nameComponents),
		nameComponents: append([]string(nil), nameComponents...),
	}, nil
}

func (r Reference) String() string {
	return r.raw
}

func (r Reference) Namespace() Namespace {
	return r.namespace
}

func (r Reference) Name() string {
	return r.name
}

func parseNamespace(component string) (Namespace, error) {
	switch component {
	case "heads":
		return NamespaceLocalBranch, nil
	case "tags":
		return NamespaceTag, nil
	case "remotes":
		return NamespaceRemote, nil
	default:
		return 0, fmt.Errorf("unsupported Git ref namespace %q", component)
	}
}
