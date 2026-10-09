package ingestionrun

import "testing"

func TestRuntimeSettingsValidateBothBounds(t *testing.T) {
	for _, value := range []int{1, 64} {
		for field := range 3 {
			settings := RuntimeSettings{2, 4, 3}
			*[]*int{&settings.MaxRunningJobs, &settings.FixedMemberConcurrency, &settings.DetailConcurrency}[field] = value
			if err := settings.Validate(); err != nil {
				t.Fatalf("field %d value %d rejected: %v", field, value, err)
			}
		}
	}
	for _, value := range []int{-1, 0, 65} {
		for field := range 3 {
			settings := RuntimeSettings{2, 4, 3}
			*[]*int{&settings.MaxRunningJobs, &settings.FixedMemberConcurrency, &settings.DetailConcurrency}[field] = value
			if err := settings.Validate(); err == nil {
				t.Fatalf("field %d value %d accepted", field, value)
			}
		}
	}
}
