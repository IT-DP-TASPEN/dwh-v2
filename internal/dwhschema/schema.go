package dwhschema

import "github.com/ibldzn/go-admin/internal/ingestion"

func CanonicalSourceKeys() ([]string, error) {
	catalog, err := ingestion.NewCatalog()
	if err != nil {
		return nil, err
	}
	jobs := catalog.Jobs()
	keys := make([]string, len(jobs))
	for index, job := range jobs {
		keys[index] = job.Key
	}
	return keys, nil
}
