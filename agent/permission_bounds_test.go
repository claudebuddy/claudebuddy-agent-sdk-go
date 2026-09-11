package agent

import (
	"reflect"
	"testing"
)

func TestIntersectToolBoundsPreservesNilAndEmptyLattice(t *testing.T) {
	tests := []struct {
		name    string
		parent  []string
		child   []string
		want    []string
		wantNil bool
	}{
		{name: "nil nil is unbounded", parent: nil, child: nil, want: nil, wantNil: true},
		{name: "nil empty adopts explicit empty", parent: nil, child: []string{}, want: []string{}, wantNil: false},
		{name: "empty nil preserves explicit empty", parent: []string{}, child: nil, want: []string{}, wantNil: false},
		{name: "disjoint bounds have explicit empty intersection", parent: []string{"Read"}, child: []string{"Bash"}, want: []string{}, wantNil: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := intersectToolBounds(tt.parent, tt.child)
			if (got == nil) != tt.wantNil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got=%#v nil=%v, want=%#v nil=%v", got, got == nil, tt.want, tt.wantNil)
			}
		})
	}
}

func TestUnionToolBoundsPreservesBothDenySources(t *testing.T) {
	tests := []struct {
		name    string
		parent  []string
		child   []string
		want    []string
		wantNil bool
	}{
		{name: "nil nil remains nil", wantNil: true},
		{name: "explicit empty remains non-nil", child: []string{}, want: []string{}},
		{name: "deduplicated stable union", parent: []string{"Bash", "Write"}, child: []string{"Write", "Read"}, want: []string{"Bash", "Write", "Read"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unionToolBounds(tt.parent, tt.child)
			if (got == nil) != tt.wantNil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got=%#v nil=%v, want=%#v nil=%v", got, got == nil, tt.want, tt.wantNil)
			}
		})
	}
}
