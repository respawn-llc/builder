package migrationcheck

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type semanticAlias string

type semanticNestedFixture struct {
	SignedWidth   int16  `json:"signed_width"`
	UnsignedWidth uint32 `json:"unsigned_width"`
}

type semanticOrdinaryFixture struct {
	TaggedName    string                            `json:"legacy_name"`
	Ignored       string                            `json:"-"`
	OptionalCount *int32                            `json:"optional_count,omitempty"`
	UnsignedWidth uint64                            `json:"unsigned_width"`
	Alias         semanticAlias                     `json:"alias"`
	Repeated      []semanticAlias                   `json:"repeated"`
	Lookup        map[string]*semanticNestedFixture `json:"lookup"`
	Nested        semanticNestedFixture             `json:"nested"`
}

type semanticTaggedUnion struct {
	Kind   string
	Text   *string
	Number *int64
}

func (u semanticTaggedUnion) MarshalJSON() ([]byte, error) {
	switch u.Kind {
	case "text":
		return json.Marshal(struct {
			Kind string  `json:"kind"`
			Text *string `json:"text"`
		}{Kind: u.Kind, Text: u.Text})
	case "number":
		return json.Marshal(struct {
			Kind   string `json:"kind"`
			Number *int64 `json:"number"`
		}{Kind: u.Kind, Number: u.Number})
	default:
		return nil, errors.New("unsupported semantic tagged union fixture")
	}
}

type semanticOpaqueToken struct {
	value string
}

type semanticReshapedOutcome struct {
	Text   *string `json:"text,omitempty"`
	Number *int64  `json:"number,omitempty"`
}

type semanticExceptionalFixture struct {
	legacyType reflect.Type
	descriptor SemanticMessageDescriptor
	mappings   []ExceptionalSemanticMapping
}

func (f semanticExceptionalFixture) LegacyType() reflect.Type {
	return f.legacyType
}

func (f semanticExceptionalFixture) Descriptor() SemanticMessageDescriptor {
	return f.descriptor
}

func (f semanticExceptionalFixture) Mappings() []ExceptionalSemanticMapping {
	return append([]ExceptionalSemanticMapping(nil), f.mappings...)
}

func TestOrdinaryMigrationSemanticFixtureCoversNestedWireGraph(t *testing.T) {
	if err := CheckOrdinaryMigrationSemantics(
		reflect.TypeFor[semanticOrdinaryFixture](),
		ordinarySemanticDescriptors(),
	); err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryMigrationSemanticFixtureReportsExactOmittedNestedFieldPaths(t *testing.T) {
	descriptors := ordinarySemanticDescriptors()
	nested := descriptors.Messages["fixture.SemanticNestedFixture"]
	nested.Fields = nested.Fields[:1]
	descriptors.Messages[nested.Path] = nested

	assertSemanticIssue(
		t,
		CheckOrdinaryMigrationSemantics(reflect.TypeFor[semanticOrdinaryFixture](), descriptors),
		IssueMissingDescriptorField,
		legacySemanticPath(reflect.TypeFor[semanticNestedFixture](), "UnsignedWidth"),
		"fixture.SemanticNestedFixture.unsigned_width",
	)
}

func TestMigrationSemanticDiagnosticIncludesExactLegacyAndDescriptorPaths(t *testing.T) {
	descriptors := ordinarySemanticDescriptors()
	nested := descriptors.Messages["fixture.SemanticNestedFixture"]
	nested.Fields = nested.Fields[:1]
	descriptors.Messages[nested.Path] = nested

	err := CheckOrdinaryMigrationSemantics(reflect.TypeFor[semanticOrdinaryFixture](), descriptors)
	legacyPath := legacySemanticPath(reflect.TypeFor[semanticNestedFixture](), "UnsignedWidth")
	descriptorPath := "fixture.SemanticNestedFixture.unsigned_width"
	want := "migration semantic check failed with 1 issue(s)\n- " +
		string(IssueMissingDescriptorField) +
		": legacy " + legacyPath +
		"; descriptor " + descriptorPath
	if err == nil || err.Error() != want {
		t.Fatalf("diagnostic = %q, want %q", err, want)
	}
}

func TestOrdinaryMigrationSemanticFixtureReportsExactMistypedNestedFieldPaths(t *testing.T) {
	descriptors := ordinarySemanticDescriptors()
	nested := descriptors.Messages["fixture.SemanticNestedFixture"]
	nested.Fields[0].Kind = SemanticUint32
	descriptors.Messages[nested.Path] = nested

	assertSemanticIssue(
		t,
		CheckOrdinaryMigrationSemantics(reflect.TypeFor[semanticOrdinaryFixture](), descriptors),
		IssueWrongScalarKind,
		legacySemanticPath(reflect.TypeFor[semanticNestedFixture](), "SignedWidth"),
		"fixture.SemanticNestedFixture.signed_width",
	)
}

func TestOrdinaryMigrationSemanticFixtureChecksEveryRequestedWireShape(t *testing.T) {
	tests := []struct {
		name           string
		mutate         func(*SemanticDescriptorSet)
		code           SemanticIssueCode
		legacyPath     string
		descriptorPath string
	}{
		{
			name: "json tag",
			mutate: func(descriptors *SemanticDescriptorSet) {
				message := descriptors.Messages["fixture.SemanticOrdinaryFixture"]
				message.Fields[0].LegacyJSONName = "tagged_name"
				descriptors.Messages[message.Path] = message
			},
			code:           IssueWrongLegacyJSONName,
			legacyPath:     legacySemanticPath(reflect.TypeFor[semanticOrdinaryFixture](), "TaggedName"),
			descriptorPath: "fixture.SemanticOrdinaryFixture.tagged_name",
		},
		{
			name: "pointer presence",
			mutate: func(descriptors *SemanticDescriptorSet) {
				message := descriptors.Messages["fixture.SemanticOrdinaryFixture"]
				message.Fields[1].Presence = false
				descriptors.Messages[message.Path] = message
			},
			code:           IssueWrongPresence,
			legacyPath:     legacySemanticPath(reflect.TypeFor[semanticOrdinaryFixture](), "OptionalCount"),
			descriptorPath: "fixture.SemanticOrdinaryFixture.optional_count",
		},
		{
			name: "unsigned width",
			mutate: func(descriptors *SemanticDescriptorSet) {
				message := descriptors.Messages["fixture.SemanticOrdinaryFixture"]
				message.Fields[2].Kind = SemanticInt64
				descriptors.Messages[message.Path] = message
			},
			code:           IssueWrongScalarKind,
			legacyPath:     legacySemanticPath(reflect.TypeFor[semanticOrdinaryFixture](), "UnsignedWidth"),
			descriptorPath: "fixture.SemanticOrdinaryFixture.unsigned_width",
		},
		{
			name: "alias",
			mutate: func(descriptors *SemanticDescriptorSet) {
				message := descriptors.Messages["fixture.SemanticOrdinaryFixture"]
				message.Fields[3].Kind = SemanticBytes
				descriptors.Messages[message.Path] = message
			},
			code:           IssueWrongScalarKind,
			legacyPath:     legacySemanticPath(reflect.TypeFor[semanticOrdinaryFixture](), "Alias"),
			descriptorPath: "fixture.SemanticOrdinaryFixture.alias",
		},
		{
			name: "repeated",
			mutate: func(descriptors *SemanticDescriptorSet) {
				message := descriptors.Messages["fixture.SemanticOrdinaryFixture"]
				message.Fields[4].Repeated = false
				descriptors.Messages[message.Path] = message
			},
			code:           IssueWrongCollectionShape,
			legacyPath:     legacySemanticPath(reflect.TypeFor[semanticOrdinaryFixture](), "Repeated"),
			descriptorPath: "fixture.SemanticOrdinaryFixture.repeated",
		},
		{
			name: "map",
			mutate: func(descriptors *SemanticDescriptorSet) {
				message := descriptors.Messages["fixture.SemanticOrdinaryFixture"]
				message.Fields[5].MapValue.Kind = SemanticString
				descriptors.Messages[message.Path] = message
			},
			code:           IssueWrongMapShape,
			legacyPath:     legacySemanticPath(reflect.TypeFor[semanticOrdinaryFixture](), "Lookup"),
			descriptorPath: "fixture.SemanticOrdinaryFixture.lookup",
		},
		{
			name: "nested message",
			mutate: func(descriptors *SemanticDescriptorSet) {
				message := descriptors.Messages["fixture.SemanticOrdinaryFixture"]
				message.Fields[6].MessagePath = "fixture.MissingNested"
				descriptors.Messages[message.Path] = message
			},
			code:           IssueWrongNestedMessage,
			legacyPath:     legacySemanticPath(reflect.TypeFor[semanticOrdinaryFixture](), "Nested"),
			descriptorPath: "fixture.SemanticOrdinaryFixture.nested",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptors := ordinarySemanticDescriptors()
			test.mutate(&descriptors)
			assertSemanticIssue(
				t,
				CheckOrdinaryMigrationSemantics(reflect.TypeFor[semanticOrdinaryFixture](), descriptors),
				test.code,
				test.legacyPath,
				test.descriptorPath,
			)
		})
	}
}

func TestOrdinaryMigrationSemanticFixtureExcludesJSONOmittedFields(t *testing.T) {
	descriptors := ordinarySemanticDescriptors()
	message := descriptors.Messages["fixture.SemanticOrdinaryFixture"]
	message.Fields = append(message.Fields, SemanticFieldDescriptor{
		Name:           "ignored",
		LegacyJSONName: "ignored",
		Kind:           SemanticString,
	})
	descriptors.Messages[message.Path] = message

	assertSemanticIssue(
		t,
		CheckOrdinaryMigrationSemantics(reflect.TypeFor[semanticOrdinaryFixture](), descriptors),
		IssueUnexpectedDescriptorField,
		legacySemanticPath(reflect.TypeFor[semanticOrdinaryFixture](), "Ignored"),
		"fixture.SemanticOrdinaryFixture.ignored",
	)
}

func TestExceptionalMigrationSemanticFixturesCoverRepresentativeShapes(t *testing.T) {
	for _, fixture := range representativeExceptionalFixtures() {
		if err := CheckExceptionalMigrationSemantics(fixture); err != nil {
			t.Fatalf("%s: %v", fixture.LegacyType(), err)
		}
	}
}

func TestExceptionalMigrationSemanticFixtureRequiresCustomMarshalClassification(t *testing.T) {
	assertSemanticIssue(
		t,
		CheckOrdinaryMigrationSemantics(
			reflect.TypeFor[semanticTaggedUnion](),
			SemanticDescriptorSet{RootMessagePath: "fixture.SemanticTaggedUnion", Messages: map[string]SemanticMessageDescriptor{
				"fixture.SemanticTaggedUnion": taggedUnionDescriptor(),
			}},
		),
		IssueRequiresExceptionalFixture,
		legacySemanticTypePath(reflect.TypeFor[semanticTaggedUnion]()),
		"fixture.SemanticTaggedUnion",
	)
}

func TestExceptionalMigrationSemanticFixtureRequiresUnexportedWireStateClassification(t *testing.T) {
	legacyType := reflect.TypeFor[semanticOpaqueToken]()
	if legacyType.Implements(reflect.TypeFor[json.Marshaler]()) ||
		reflect.PointerTo(legacyType).Implements(reflect.TypeFor[json.Marshaler]()) {
		t.Fatal("unexported-state fixture must not rely on custom marshaling")
	}
	assertSemanticIssue(
		t,
		CheckOrdinaryMigrationSemantics(
			legacyType,
			SemanticDescriptorSet{RootMessagePath: "fixture.SemanticOpaqueToken", Messages: map[string]SemanticMessageDescriptor{
				"fixture.SemanticOpaqueToken": opaqueTokenDescriptor(),
			}},
		),
		IssueRequiresExceptionalFixture,
		legacySemanticTypePath(legacyType),
		"fixture.SemanticOpaqueToken",
	)
}

func TestExceptionalMigrationSemanticFixtureMapsUnexportedWireStateExactly(t *testing.T) {
	fixture := representativeExceptionalFixtures()[1]
	mappings := fixture.Mappings()
	if len(mappings) != 1 {
		t.Fatalf("unexported-state mappings = %d, want 1", len(mappings))
	}
	wantLegacyPath := legacySemanticTypePath(reflect.TypeFor[semanticOpaqueToken]()) + ".value"
	if mappings[0].LegacyPath != wantLegacyPath ||
		mappings[0].DescriptorPath != "fixture.SemanticOpaqueToken.value" {
		t.Fatalf("unexported-state mapping = %+v", mappings[0])
	}
}

func TestExceptionalMigrationSemanticFixtureRejectsDuplicateVariantMappings(t *testing.T) {
	tests := []struct {
		name string
		code SemanticIssueCode
		edit func(*semanticExceptionalFixture)
	}{
		{
			name: "legacy path",
			code: IssueDuplicateExceptionalLegacy,
			edit: func(fixture *semanticExceptionalFixture) {
				fixture.mappings[1].LegacyPath = fixture.mappings[0].LegacyPath
			},
		},
		{
			name: "descriptor path",
			code: IssueDuplicateExceptionalTarget,
			edit: func(fixture *semanticExceptionalFixture) {
				fixture.mappings[1].DescriptorPath = fixture.mappings[0].DescriptorPath
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := representativeExceptionalFixtures()[0].(semanticExceptionalFixture)
			test.edit(&fixture)
			assertSemanticIssue(
				t,
				CheckExceptionalMigrationSemantics(fixture),
				test.code,
				fixture.mappings[0].LegacyPath,
				fixture.mappings[0].DescriptorPath,
			)
		})
	}
}

func TestExceptionalMigrationSemanticFixtureReportsMissingCustomUnionVariantPaths(t *testing.T) {
	fixture := representativeExceptionalFixtures()[0].(semanticExceptionalFixture)
	fixture.descriptor.Fields = fixture.descriptor.Fields[:1]

	assertSemanticIssue(
		t,
		CheckExceptionalMigrationSemantics(fixture),
		IssueMissingExceptionalMapping,
		legacySemanticTypePath(reflect.TypeFor[semanticTaggedUnion]())+".number",
		"fixture.SemanticTaggedUnion.number",
	)
}

func TestExceptionalMigrationSemanticFixtureReportsMissingOneofReshapeVariantPaths(t *testing.T) {
	fixture := representativeExceptionalFixtures()[2].(semanticExceptionalFixture)
	fixture.descriptor.Fields = fixture.descriptor.Fields[:1]

	assertSemanticIssue(
		t,
		CheckExceptionalMigrationSemantics(fixture),
		IssueMissingExceptionalMapping,
		legacySemanticPath(reflect.TypeFor[semanticReshapedOutcome](), "Number"),
		"fixture.SemanticReshapedOutcome.number",
	)
}

func TestMigrationSemanticDescriptorFixtureCoversOrdinaryNestedGraph(t *testing.T) {
	if err := CheckOrdinaryMigrationSemantics(
		reflect.TypeFor[semanticOrdinaryFixture](),
		ordinarySemanticDescriptors(),
	); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationSemanticFixtureCoversCustomTaggedUnion(t *testing.T) {
	assertExceptionalDescriptor(t, representativeExceptionalFixtures()[0])
}

func TestMigrationSemanticFixtureCoversWireSignificantUnexportedState(t *testing.T) {
	assertExceptionalDescriptor(t, representativeExceptionalFixtures()[1])
}

func TestMigrationSemanticFixtureCoversIntentionalOneofReshape(t *testing.T) {
	assertExceptionalDescriptor(t, representativeExceptionalFixtures()[2])
}

func assertExceptionalDescriptor(t *testing.T, fixture ExceptionalSemanticFixture) {
	t.Helper()
	if err := CheckExceptionalMigrationSemantics(fixture); err != nil {
		t.Fatal(err)
	}
}

func ordinarySemanticDescriptors() SemanticDescriptorSet {
	return SemanticDescriptorSet{
		RootMessagePath: "fixture.SemanticOrdinaryFixture",
		Messages: map[string]SemanticMessageDescriptor{
			"fixture.SemanticOrdinaryFixture": {
				Path: "fixture.SemanticOrdinaryFixture",
				Fields: []SemanticFieldDescriptor{
					{Name: "tagged_name", LegacyJSONName: "legacy_name", Kind: SemanticString},
					{Name: "optional_count", LegacyJSONName: "optional_count", Kind: SemanticInt32, Presence: true},
					{Name: "unsigned_width", LegacyJSONName: "unsigned_width", Kind: SemanticUint64},
					{Name: "alias", LegacyJSONName: "alias", Kind: SemanticString},
					{Name: "repeated", LegacyJSONName: "repeated", Kind: SemanticString, Repeated: true},
					{
						Name:           "lookup",
						LegacyJSONName: "lookup",
						MapKey:         &SemanticValueDescriptor{Kind: SemanticString},
						MapValue: &SemanticValueDescriptor{
							Kind:        SemanticMessage,
							MessagePath: "fixture.SemanticNestedFixture",
							Presence:    true,
						},
					},
					{
						Name:           "nested",
						LegacyJSONName: "nested",
						Kind:           SemanticMessage,
						MessagePath:    "fixture.SemanticNestedFixture",
					},
				},
			},
			"fixture.SemanticNestedFixture": {
				Path: "fixture.SemanticNestedFixture",
				Fields: []SemanticFieldDescriptor{
					{Name: "signed_width", LegacyJSONName: "signed_width", Kind: SemanticInt16},
					{Name: "unsigned_width", LegacyJSONName: "unsigned_width", Kind: SemanticUint32},
				},
			},
		},
	}
}

func representativeExceptionalFixtures() []ExceptionalSemanticFixture {
	return []ExceptionalSemanticFixture{
		semanticExceptionalFixture{
			legacyType: reflect.TypeFor[semanticTaggedUnion](),
			descriptor: taggedUnionDescriptor(),
			mappings: []ExceptionalSemanticMapping{
				{
					LegacyPath:     legacySemanticTypePath(reflect.TypeFor[semanticTaggedUnion]()) + ".text",
					DescriptorPath: "fixture.SemanticTaggedUnion.text",
					Presence:       true,
					Oneof:          "value",
				},
				{
					LegacyPath:     legacySemanticTypePath(reflect.TypeFor[semanticTaggedUnion]()) + ".number",
					DescriptorPath: "fixture.SemanticTaggedUnion.number",
					Presence:       true,
					Oneof:          "value",
				},
			},
		},
		semanticExceptionalFixture{
			legacyType: reflect.TypeFor[semanticOpaqueToken](),
			descriptor: opaqueTokenDescriptor(),
			mappings: []ExceptionalSemanticMapping{{
				LegacyPath:     legacySemanticTypePath(reflect.TypeFor[semanticOpaqueToken]()) + ".value",
				DescriptorPath: "fixture.SemanticOpaqueToken.value",
			}},
		},
		semanticExceptionalFixture{
			legacyType: reflect.TypeFor[semanticReshapedOutcome](),
			descriptor: SemanticMessageDescriptor{
				Path: "fixture.SemanticReshapedOutcome",
				Fields: []SemanticFieldDescriptor{
					{Name: "text", LegacyJSONName: "text", Kind: SemanticString, Presence: true, Oneof: "outcome"},
					{Name: "number", LegacyJSONName: "number", Kind: SemanticInt64, Presence: true, Oneof: "outcome"},
				},
			},
			mappings: []ExceptionalSemanticMapping{
				{
					LegacyPath:     legacySemanticPath(reflect.TypeFor[semanticReshapedOutcome](), "Text"),
					DescriptorPath: "fixture.SemanticReshapedOutcome.text",
					Presence:       true,
					Oneof:          "outcome",
				},
				{
					LegacyPath:     legacySemanticPath(reflect.TypeFor[semanticReshapedOutcome](), "Number"),
					DescriptorPath: "fixture.SemanticReshapedOutcome.number",
					Presence:       true,
					Oneof:          "outcome",
				},
			},
		},
	}
}

func taggedUnionDescriptor() SemanticMessageDescriptor {
	return SemanticMessageDescriptor{
		Path: "fixture.SemanticTaggedUnion",
		Fields: []SemanticFieldDescriptor{
			{Name: "text", LegacyJSONName: "text", Kind: SemanticString, Presence: true, Oneof: "value"},
			{Name: "number", LegacyJSONName: "number", Kind: SemanticInt64, Presence: true, Oneof: "value"},
		},
	}
}

func opaqueTokenDescriptor() SemanticMessageDescriptor {
	return SemanticMessageDescriptor{
		Path: "fixture.SemanticOpaqueToken",
		Fields: []SemanticFieldDescriptor{
			{Name: "value", LegacyJSONName: "value", Kind: SemanticString},
		},
	}
}

func assertSemanticIssue(
	t *testing.T,
	err error,
	wantCode SemanticIssueCode,
	wantLegacyPath string,
	wantDescriptorPath string,
) {
	t.Helper()
	if err == nil {
		t.Fatal("semantic migration check unexpectedly succeeded")
	}
	var semanticError *SemanticError
	if !errors.As(err, &semanticError) {
		t.Fatalf("error type = %T, want *SemanticError", err)
	}
	for _, issue := range semanticError.Issues {
		if issue.Code != wantCode {
			continue
		}
		if issue.LegacyPath != wantLegacyPath || issue.DescriptorPath != wantDescriptorPath {
			t.Fatalf(
				"semantic issue paths = legacy %q descriptor %q, want legacy %q descriptor %q",
				issue.LegacyPath,
				issue.DescriptorPath,
				wantLegacyPath,
				wantDescriptorPath,
			)
		}
		return
	}
	t.Fatalf("semantic issues = %+v, want code %q", semanticError.Issues, wantCode)
}
