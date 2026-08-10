package pagination

import "testing"

func TestNew(t *testing.T) {
	page := New(3, 20, 45)
	if page.Page != 3 || page.TotalPages != 3 || page.Previous != 2 || page.Next != 0 || page.Offset() != 40 {
		t.Fatalf("unexpected page: %+v", page)
	}
	if got := New(99, 0, 0); got.Page != 1 || got.PerPage != 1 || got.TotalPages != 1 {
		t.Fatalf("unexpected clamped page: %+v", got)
	}
}
