package model

import "testing"

func TestEquivalentPersonNamesCoversGeneralSimplifiedTraditionalVariants(t *testing.T) {
	tests := []struct {
		simplified  string
		traditional string
		directAlias bool
	}{
		{simplified: "三田真铃", traditional: "三田真鈴", directAlias: true},
		{simplified: "濑名光", traditional: "瀨名光", directAlias: true},
		{simplified: "后藤里香", traditional: "後藤里香"},
	}
	for _, test := range tests {
		if NormalizeChineseVariants(test.traditional) != test.simplified {
			t.Fatalf("normalize %q = %q, want %q", test.traditional, NormalizeChineseVariants(test.traditional), test.simplified)
		}
		aliases := EquivalentPersonNames(test.simplified)
		found := false
		for _, alias := range aliases {
			if alias == test.traditional {
				found = true
				break
			}
		}
		if test.directAlias && !found {
			t.Fatalf("aliases for %q do not include %q: %v", test.simplified, test.traditional, aliases)
		}
		if NormalizeMediaSearchText(test.simplified) != NormalizeMediaSearchText(test.traditional) {
			t.Fatalf("search normalization differs for %q and %q", test.simplified, test.traditional)
		}
	}
}
