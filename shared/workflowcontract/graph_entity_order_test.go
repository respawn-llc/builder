package workflowcontract

import "testing"

func TestCompareGraphEntityIdentityOrdersTypeBeforePersistentID(t *testing.T) {
	tests := []struct {
		name                                 string
		leftType, leftID, rightType, rightID string
		want                                 int
	}{
		{name: "entity type", leftType: "edge", leftID: "z", rightType: "node", rightID: "a", want: -1},
		{name: "persistent ID", leftType: "node", leftID: "a", rightType: "node", rightID: "b", want: -1},
		{name: "equal", leftType: "node", leftID: "a", rightType: "node", rightID: "a", want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := CompareGraphEntityIdentity(test.leftType, test.leftID, test.rightType, test.rightID)
			if got != test.want {
				t.Fatalf("CompareGraphEntityIdentity = %d, want %d", got, test.want)
			}
		})
	}
}
