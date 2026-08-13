package migrationcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

type WellKnownScalarMapping string

const WellKnownProtobufDuration WellKnownScalarMapping = "google.protobuf.Duration"

type WellKnownScalarSignoff struct {
	Identity Identity
	Mapping  WellKnownScalarMapping
}

type DomainDeclarationSignoff struct {
	Domain           string
	Classification   DeclarationClassification
	WellKnownScalars []WellKnownScalarSignoff
}

type DomainSignoffError struct {
	Identity Identity
	Domains  []string
}

func (e *DomainSignoffError) Error() string {
	return fmt.Sprintf(
		"execution-target identity %s is classified in multiple domains: %s",
		e.Identity,
		strings.Join(e.Domains, ", "),
	)
}

func MergeDomainDeclarationSignoffs(signoffs []DomainDeclarationSignoff) (DeclarationClassification, error) {
	var merged DeclarationClassification
	scalarDomains := make(map[Identity]string)
	validatorDomains := make(map[Identity]string)
	wellKnownDomains := make(map[Identity]string)
	for _, signoff := range signoffs {
		for _, scalar := range signoff.Classification.Scalars {
			if previous, exists := scalarDomains[scalar.Identity]; exists {
				return DeclarationClassification{}, &DomainSignoffError{
					Identity: scalar.Identity,
					Domains:  []string{previous, signoff.Domain},
				}
			}
			scalarDomains[scalar.Identity] = signoff.Domain
			merged.Scalars = append(merged.Scalars, scalar)
		}
		for _, wellKnown := range signoff.WellKnownScalars {
			if previous, exists := wellKnownDomains[wellKnown.Identity]; exists {
				return DeclarationClassification{}, &DomainSignoffError{
					Identity: wellKnown.Identity,
					Domains:  []string{previous, signoff.Domain},
				}
			}
			wellKnownDomains[wellKnown.Identity] = signoff.Domain
			if err := validateWellKnownScalarSignoff(signoff, wellKnown); err != nil {
				return DeclarationClassification{}, err
			}
		}
		for _, validator := range signoff.Classification.Validators {
			if previous, exists := validatorDomains[validator.Identity]; exists {
				return DeclarationClassification{}, &DomainSignoffError{
					Identity: validator.Identity,
					Domains:  []string{previous, signoff.Domain},
				}
			}
			validatorDomains[validator.Identity] = signoff.Domain
			merged.Validators = append(merged.Validators, validator)
		}
	}
	sort.Slice(merged.Scalars, func(left, right int) bool {
		return merged.Scalars[left].Identity.String() < merged.Scalars[right].Identity.String()
	})
	sort.Slice(merged.Validators, func(left, right int) bool {
		return merged.Validators[left].Identity.String() < merged.Validators[right].Identity.String()
	})
	return merged, nil
}

func validateWellKnownScalarSignoff(
	domain DomainDeclarationSignoff,
	wellKnown WellKnownScalarSignoff,
) error {
	durationIdentity := typeIdentity("time", "Duration")
	if wellKnown.Identity != durationIdentity ||
		wellKnown.Mapping != WellKnownProtobufDuration {
		return fmt.Errorf(
			"unsupported execution-target well-known scalar mapping: %s -> %s",
			wellKnown.Identity,
			wellKnown.Mapping,
		)
	}
	for _, scalar := range domain.Classification.Scalars {
		if scalar.Identity != wellKnown.Identity {
			continue
		}
		if scalar.Kind != ScalarProtobufDuration || len(scalar.EnumMembers) != 0 {
			return fmt.Errorf(
				"well-known scalar %s must use the Protobuf Duration classification",
				wellKnown.Identity,
			)
		}
		return nil
	}
	return fmt.Errorf(
		"well-known scalar mapping has no matching scalar classification: %s",
		wellKnown.Identity,
	)
}

func declarationClassificationFingerprint(classification DeclarationClassification) string {
	var canonical strings.Builder
	for _, scalar := range classification.Scalars {
		fmt.Fprintf(&canonical, "scalar\t%s\t%s\n", scalar.Identity, scalar.Kind)
		for _, member := range scalar.EnumMembers {
			fmt.Fprintf(
				&canonical,
				"member\t%s\t%s\t%t\n",
				member.GoConstant,
				member.DescriptorName,
				member.IntentionalRename,
			)
		}
	}
	for _, validator := range classification.Validators {
		fmt.Fprintf(
			&canonical,
			"validator\t%s\t%s\t%s\n",
			validator.Identity,
			validator.Fingerprint,
			validator.Kind,
		)
	}
	sum := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(sum[:])
}

func ExecutionTargetDomainSignoffs() []DomainDeclarationSignoff {
	return executionTargetDomainSignoffs()
}

func scalarSignoff(packagePath, typeName string, kind ScalarClassificationKind, members ...EnumMemberClassification) ScalarClassification {
	return ScalarClassification{
		Identity:    typeIdentity(packagePath, typeName),
		Kind:        kind,
		EnumMembers: members,
	}
}

func enumMember(goConstant, descriptorName string) EnumMemberClassification {
	return EnumMemberClassification{GoConstant: goConstant, DescriptorName: descriptorName}
}

func enumAlias(goConstant, canonicalDescriptorName string) EnumMemberClassification {
	return EnumMemberClassification{
		GoConstant:        goConstant,
		DescriptorName:    canonicalDescriptorName,
		IntentionalRename: true,
	}
}

func validatorSignoff(packagePath, typeName, methodName, fingerprint string) ValidatorClassification {
	return ValidatorClassification{
		Identity: Identity{
			PackagePath: packagePath,
			TypeName:    typeName,
			MemberName:  methodName,
			Kind:        IdentityFunction,
		},
		Fingerprint: fingerprint,
		Kind:        ValidatorMessageLocal,
	}
}
