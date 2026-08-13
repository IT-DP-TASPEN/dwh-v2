package ingestion

import (
	"fmt"
	"regexp"
)

type JobCategory string

const (
	CategoryFixed  JobCategory = "fixed"
	CategoryEOD    JobCategory = "maintenance_eod"
	CategoryCBR    JobCategory = "maintenance_cbr"
	CategoryDetail JobCategory = "detail"
)

type JobDefinition struct {
	Key         string
	Name        string
	Category    JobCategory
	Active      bool
	Fixed       *FixedDefinition
	Maintenance *MaintenanceDefinition
}

type Catalog struct {
	jobs  []JobDefinition
	byKey map[string]JobDefinition
}

func NewCatalog() (Catalog, error) {
	jobs := make([]JobDefinition, 0, 36)
	fixedDefinitions := FixedDefinitions()
	if err := validateFixedDefinitions(fixedDefinitions); err != nil {
		return Catalog{}, err
	}
	for _, definition := range fixedDefinitions {
		copy := definition
		jobs = append(jobs, JobDefinition{Key: copy.Key, Name: copy.Name, Category: CategoryFixed, Active: true, Fixed: &copy})
	}
	maintenanceDefinitions := MaintenanceDefinitions()
	if err := validateMaintenanceDefinitions(maintenanceDefinitions); err != nil {
		return Catalog{}, err
	}
	for _, definition := range maintenanceDefinitions {
		copy := definition
		category := CategoryEOD
		if copy.Kind == MaintenanceCBR {
			category = CategoryCBR
		}
		jobs = append(jobs, JobDefinition{Key: copy.Key, Name: copy.Name, Category: category, Active: true, Maintenance: &copy})
	}
	for _, detail := range []struct{ key, name string }{
		{"cif_detail", "CIF Detail"},
		{"saving_detail", "Saving Detail"},
		{"time_deposit_detail", "Time Deposit Detail"},
		{"loan_detail", "Loan Detail"},
	} {
		jobs = append(jobs, JobDefinition{Key: detail.key, Name: detail.name, Category: CategoryDetail, Active: true})
	}
	if len(jobs) != 36 {
		return Catalog{}, fmt.Errorf("canonical catalog has %d jobs, want 36", len(jobs))
	}
	byKey := make(map[string]JobDefinition, len(jobs))
	for _, job := range jobs {
		if !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(job.Key) {
			return Catalog{}, fmt.Errorf("invalid job key %q", job.Key)
		}
		if _, exists := byKey[job.Key]; exists {
			return Catalog{}, fmt.Errorf("duplicate job key %q", job.Key)
		}
		byKey[job.Key] = job
	}
	return Catalog{jobs: jobs, byKey: byKey}, nil
}

func validateFixedDefinitions(definitions []FixedDefinition) error {
	if len(definitions) != 8 {
		return fmt.Errorf("fixed catalog has %d definitions, want 8", len(definitions))
	}
	seen := map[string]struct{}{}
	for _, definition := range definitions {
		if definition.Key == "" || definition.Name == "" || definition.FincloudReportName == "" || len(definition.RequiredHeaders) == 0 || definition.MaxChunkDays != 30 {
			return fmt.Errorf("fixed definition %q is incomplete", definition.Key)
		}
		if _, duplicate := seen[definition.Key]; duplicate {
			return fmt.Errorf("duplicate fixed definition %q", definition.Key)
		}
		seen[definition.Key] = struct{}{}
		if definition.LocationStrategy != SingleRequestAllLocationsEmpty && definition.LocationStrategy != PerLocation {
			return fmt.Errorf("%s has invalid location strategy", definition.Key)
		}
		if definition.AccountCodeStrategy != NoAccountCodeStrategy && definition.AccountCodeStrategy != AllAccountCodes {
			return fmt.Errorf("%s has invalid account-code strategy", definition.Key)
		}
		if definition.LocationStrategy == PerLocation && definition.AccountCodeStrategy != NoAccountCodeStrategy {
			return fmt.Errorf("%s has conflicting internal dimensions", definition.Key)
		}
		if definition.LocationStrategy == PerLocation && !definition.SourceLocationID {
			return fmt.Errorf("%s must preserve request-location provenance", definition.Key)
		}
		if definition.LocationStrategy != PerLocation && definition.SourceLocationID {
			return fmt.Errorf("%s has unexpected request-location provenance", definition.Key)
		}
	}
	return nil
}

func (catalog Catalog) Jobs() []JobDefinition { return append([]JobDefinition(nil), catalog.jobs...) }

func (catalog Catalog) Find(key string) (JobDefinition, bool) {
	job, found := catalog.byKey[key]
	return job, found
}

func FixedDefinitions() []FixedDefinition {
	return []FixedDefinition{
		fixed("cif_opening_report", "CIF Opening Report", "CIF Opening Report", SingleRequestAllLocationsEmpty, NoAccountCodeStrategy, cifOpeningHeaders()),
		fixed("journal_transaction_report", "Journal Transaction Report", "Journal Transaction csv", SingleRequestAllLocationsEmpty, NoAccountCodeStrategy, journalHeaders()),
		fixedSnapshot("balance_sheet_report", "Balance Sheet Report", "Balance Sheet Report csv", PerLocation, balanceSheetHeaders(), true),
		fixed("profit_loss_statement", "Profit and Loss Statement", "Profit and Loss Statement csv", PerLocation, NoAccountCodeStrategy, profitLossHeaders(), true),
		fixed("coa_movement_report", "CoA Movement Report", "CoA Movement Report csv", SingleRequestAllLocationsEmpty, AllAccountCodes, coaMovementHeaders()),
		fixed("fund_distribution_report", "Fund Distribution Report", "Fund Distribution Report csv", SingleRequestAllLocationsEmpty, NoAccountCodeStrategy, fundDistributionHeaders()),
		fixed("vault_mutation_report", "Vault Mutation Report", "Vault Mutation Report csv", SingleRequestAllLocationsEmpty, NoAccountCodeStrategy, vaultMutationHeaders()),
		fixed("teller_mutation_report", "Teller Mutation Report", "Teller Mutation Report (Teller's Blotter) csv", SingleRequestAllLocationsEmpty, NoAccountCodeStrategy, tellerMutationHeaders()),
	}
}

func fixed(key, name, report string, location LocationStrategy, account AccountCodeStrategy, headers []string, sourceLocation ...bool) FixedDefinition {
	return FixedDefinition{Key: key, Name: name, FincloudReportName: report, RequiredHeaders: headers, LocationStrategy: location, AccountCodeStrategy: account, SourceLocationID: len(sourceLocation) > 0 && sourceLocation[0], MaxChunkDays: 30}
}

func fixedSnapshot(key, name, report string, location LocationStrategy, headers []string, sourceLocation bool) FixedDefinition {
	definition := fixed(key, name, report, location, NoAccountCodeStrategy, headers, sourceLocation)
	definition.SnapshotDate = true
	return definition
}

func cifOpeningHeaders() []string {
	return []string{"CIF No", "CIF Alt No", "Customer Name", "Alias Name", "Mobile Phone", "Home Phone", "Religion", "Formal Education", "Employee ID / Retired ID", "Age", "Customer Type", "Occupation", "Company Name", "Office Address", "Total Monthly Income", "Customer Status", "National ID No", "Tax ID", "Emails", "Birth Date", "Birth Place", "Home Address", "Home Urban Village", "Home  Sub-Distric", "Home  City", "Home Province", "Home Postal Code", "Mother Maiden Name", "Gender", "Marital Status", "Customer Data Attachment", "Branch Code", "Officer Create", "Register Date"}
}
func journalHeaders() []string {
	return []string{"Branch", "Journal ID", "Transaction Date", "Transaction Type", "Reference Number", "Description", "Officer Create", "Account No", "Customer Name", "Customer No", "Account Alternate No", "Currency", "CoA No", "CoA Name", "Debit", "Credit", "Transaction Value", "Transaction Code", "Time", "Create Date"}
}
func balanceSheetHeaders() []string {
	return []string{"Branch", "CoA No", "Chart of Account", "Beginning Balance", "Debit Transaction", "Credit Transaction", "Last Balance"}
}
func profitLossHeaders() []string {
	return []string{"CoA No", "Chart of Account", "Beginning Balance", "Debit", "Credit", "Last Balance"}
}
func coaMovementHeaders() []string {
	return []string{"CoA No", "Branch", "Date", "Journal ID", "Beginning Balance", "Debit", "Credit", "Last Balance", "Reference No", "Transaction Type", "Description", "Officer Create", "User Authorize", "Create Date"}
}
func fundDistributionHeaders() []string {
	return []string{"Savings Alt No", "Journal Date", "Fund Distribution Name", "Transaction No", "CIF No", "Customer Name", "Savings No", "Savings Product", "Transaction Type", "Transaction Amount", "Branch", "Description"}
}
func vaultMutationHeaders() []string {
	return []string{"branch", "tellername", "transactiontype", "currency", "beginningbalance", "lastbalance", "debit", "credit", "officer", "datetime"}
}
func tellerMutationHeaders() []string {
	return []string{"referencenumber", "accountnumber", "customername", "transactiontype", "beginningbalance", "debit", "credit", "lastbalance", "branch", "tellerid", "userauthorization", "useroverride", "transactiondate"}
}
