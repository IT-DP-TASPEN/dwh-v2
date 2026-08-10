package pagination

type Page struct {
	Page       int
	PerPage    int
	Total      int64
	TotalPages int
	Previous   int
	Next       int
}

func New(page, perPage int, total int64) Page {
	if perPage < 1 {
		perPage = 1
	}
	totalPages := max(int((total+int64(perPage)-1)/int64(perPage)), 1)
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	result := Page{Page: page, PerPage: perPage, Total: total, TotalPages: totalPages}
	if page > 1 {
		result.Previous = page - 1
	}
	if page < totalPages {
		result.Next = page + 1
	}
	return result
}

func (page Page) Offset() int {
	return (page.Page - 1) * page.PerPage
}
