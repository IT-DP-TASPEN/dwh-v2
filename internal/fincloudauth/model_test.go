package fincloudauth

import "testing"

func TestProfileIdentifiersPreserveExactValueAndRejectBoundaryWhitespace(t *testing.T) {
	valid := Input{Name: "Primary", Username: "CaseSensitive", Password: " secret ", RoleID: "Role-A", LocationID: "Location-01"}
	if err := validateInput(valid, true); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Input){
		func(input *Input) { input.Username = " CaseSensitive" },
		func(input *Input) { input.RoleID = "Role-A " },
		func(input *Input) { input.LocationID = "\tLocation-01" },
	} {
		input := valid
		mutate(&input)
		if err := validateInput(input, true); err == nil {
			t.Fatalf("invalid identifiers accepted: %+v", input)
		}
	}
}
