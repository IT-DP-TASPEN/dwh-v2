package ingestion

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const maxMySQLIdentifierLength = 64

type MaintenanceKind string
type MaintenanceIdentity string
type SchemaMode string
type MaintenanceDisposition string

const (
	MaintenanceEOD MaintenanceKind = "eod"
	MaintenanceCBR MaintenanceKind = "cbr"

	BusinessKeyIdentity MaintenanceIdentity = "business_key"
	RowNumberIdentity   MaintenanceIdentity = "row_number"

	DynamicAdditive SchemaMode = "dynamic_additive"

	MaintenanceRegistered MaintenanceDisposition = "registered"
	MaintenanceDone       MaintenanceDisposition = "done_control"
	MaintenanceSIPENDAR   MaintenanceDisposition = "sipendar_unsupported"
	MaintenanceUnknown    MaintenanceDisposition = "unknown"
)

type DynamicSchemaPolicy struct {
	Mode                   SchemaMode
	CreateTableOnFirstLoad bool
	ReuseKnownColumns      bool
	AddMissingColumns      bool
	AddColumnSQLType       string
	AllowDrop              bool
	AllowRename            bool
	AllowTypeMutation      bool
}

var DynamicAdditivePolicy = DynamicSchemaPolicy{
	Mode: DynamicAdditive, CreateTableOnFirstLoad: true, ReuseKnownColumns: true,
	AddMissingColumns: true, AddColumnSQLType: "TEXT NULL",
}

type MaintenanceDefinition struct {
	Key                string
	Name               string
	Kind               MaintenanceKind
	FilePattern        *regexp.Regexp
	TableName          string
	Identity           MaintenanceIdentity
	BusinessKeyColumns []string
	SchemaMode         SchemaMode
	FixtureGapAccepted bool
}

func validateMaintenanceDefinitions(definitions []MaintenanceDefinition) error {
	seenKeys, seenTables, seenPatterns := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	identifier := regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	for _, definition := range definitions {
		if !identifier.MatchString(definition.Key) || !identifier.MatchString(definition.TableName) || len(definition.TableName) > maxMySQLIdentifierLength {
			return fmt.Errorf("invalid maintenance definition %q", definition.Key)
		}
		if definition.Kind != MaintenanceEOD && definition.Kind != MaintenanceCBR || definition.FilePattern == nil || definition.SchemaMode != DynamicAdditive {
			return fmt.Errorf("maintenance definition %s has invalid source contract", definition.Key)
		}
		if _, duplicate := seenKeys[definition.Key]; duplicate {
			return fmt.Errorf("duplicate maintenance key %q", definition.Key)
		}
		if _, duplicate := seenTables[definition.TableName]; duplicate {
			return fmt.Errorf("duplicate maintenance table %q", definition.TableName)
		}
		patternKey := string(definition.Kind) + "\x00" + definition.FilePattern.String()
		if _, duplicate := seenPatterns[patternKey]; duplicate {
			return fmt.Errorf("duplicate maintenance filename matcher for %s", definition.Key)
		}
		seenKeys[definition.Key], seenTables[definition.TableName], seenPatterns[patternKey] = struct{}{}, struct{}{}, struct{}{}
		switch definition.Identity {
		case BusinessKeyIdentity:
			if len(definition.BusinessKeyColumns) == 0 {
				return fmt.Errorf("%s requires business keys", definition.Key)
			}
		case RowNumberIdentity:
			if len(definition.BusinessKeyColumns) != 0 {
				return fmt.Errorf("%s row identity cannot have business keys", definition.Key)
			}
		default:
			return fmt.Errorf("%s has invalid identity", definition.Key)
		}
	}
	return nil
}

func MaintenanceDefinitions() []MaintenanceDefinition {
	return []MaintenanceDefinition{
		maintenance("eod_cif_opening_report_full", "EOD CIF Opening Report (Full)", MaintenanceEOD, "CIF Opening Report (Full).csv", "fincloud_eod_cif_opening_report_full", BusinessKeyIdentity, "cif_no"),
		maintenance("eod_detail_outstanding_rekening_pinjaman", "EOD DetailOutstandingRekeningPinjaman", MaintenanceEOD, "DetailOutstandingRekeningPinjaman.csv", "fincloud_eod_detail_outstanding_rekening_pinjaman", BusinessKeyIdentity, "no_rekening"),
		maintenance("eod_laporan_pelunasan_pinjaman_sebelum_jt", "EOD LaporanPelunasanPinjamanSebelumJT", MaintenanceEOD, "LaporanPelunasanPinjamanSebelumJT.csv", "fincloud_eod_laporan_pelunasan_pinjaman_sebelum_jt", BusinessKeyIdentity, "norekening", "tgl_pelunasan"),
		maintenance("eod_laporan_pembayaran_angsuran", "EOD LaporanPembayaranAngsuran", MaintenanceEOD, "LaporanPembayaranAngsuran.csv", "fincloud_eod_laporan_pembayaran_angsuran", BusinessKeyIdentity, "norekening", "tglbayar", "bayar_pokok", "bayar_bunga", "bayar_denda"),
		maintenance("eod_laporan_pencairan_pinjaman", "EOD LaporanPencairanPinjaman", MaintenanceEOD, "LaporanPencairanPinjaman.csv", "fincloud_eod_laporan_pencairan_pinjaman", BusinessKeyIdentity, "norekening"),
		maintenance("eod_laporan_pinjaman_akan_jatuh_tempo", "EOD LaporanPinjamanAkanJatuhTempo", MaintenanceEOD, "LaporanPinjamanAkanJatuhTempo.csv", "fincloud_eod_laporan_pinjaman_akan_jatuh_tempo", BusinessKeyIdentity, "no_rekening", "angsuran_ke"),
		maintenance("eod_loan_write_off_report", "EOD Loan Write Off Report", MaintenanceEOD, "Loan Write Off Report.csv", "fincloud_eod_loan_write_off_report", BusinessKeyIdentity, "loan_no"),
		maintenance("eod_savings_account_api_transaction", "EOD Savings Account API Transaction", MaintenanceEOD, "Savings Account API Transaction.csv", "fincloud_eod_savings_account_api_transaction", RowNumberIdentity),
		maintenance("eod_savings_account_closing_report", "EOD Savings Account Closing Report", MaintenanceEOD, "Savings Account Closing Report.csv", "fincloud_eod_savings_account_closing_report", BusinessKeyIdentity, "account_number"),
		maintenance("eod_savings_account_opening_report", "EOD Savings Account Opening Report", MaintenanceEOD, "Savings Account Opening Report.csv", "fincloud_eod_savings_account_opening_report", BusinessKeyIdentity, "account_number"),
		maintenance("eod_savings_account_balance_report", "EOD Savings Account Balance Report", MaintenanceEOD, "DetailSaldoRekeningTabungan.csv", "fincloud_eod_savings_account_balance_report", BusinessKeyIdentity, "no_rekening"),
		maintenance("eod_loan_will_due_report", "EOD Loan Will Due Report", MaintenanceEOD, "Loan Will Due Report.csv", "fincloud_eod_loan_will_due_report", BusinessKeyIdentity, "account_number", "maturity_date"),
		fixtureGap(maintenance("eod_savings_balance_details_report", "EOD Savings Balance Details Report", MaintenanceEOD, "Savings Balance Details Report.csv", "fincloud_eod_savings_balance_details_report", BusinessKeyIdentity, "account_no")),
		fixtureGap(maintenance("eod_time_deposit_account_balance_details", "EOD Time Deposit Account Balance Details", MaintenanceEOD, "Time Deposit Account Balance Details.csv", "fincloud_eod_time_deposit_account_balance_details", BusinessKeyIdentity, "account_no")),
		fixtureGap(maintenance("eod_time_deposit_closing_report", "EOD Time Deposit Closing Report", MaintenanceEOD, "Time Deposit Closing Report.csv", "fincloud_eod_time_deposit_closing_report", BusinessKeyIdentity, "account_no")),
		fixtureGap(maintenance("eod_time_deposit_placement_report", "EOD Time Deposit Placement Report", MaintenanceEOD, "Time Deposit Placement Report.csv", "fincloud_eod_time_deposit_placement_report", BusinessKeyIdentity, "account_no")),
		fixtureGap(maintenance("eod_savings_balance_details_report_rak", "EOD Savings Balance Details Report RAK", MaintenanceEOD, "savings_balance_details_report_rak.csv", "fincloud_eod_savings_balance_details_report_rak", BusinessKeyIdentity, "account_no")),
		maintenance("cbr_balance_sheet", "CBR Balance Sheet", MaintenanceCBR, "cbr_balance_sheet.csv", "fincloud_cbr_balance_sheet", BusinessKeyIdentity, "account_id", "account_name"),
		maintenance("cbr_arrears", "CBR Arrears", MaintenanceCBR, "cbrarrears.csv", "fincloud_cbr_arrears", BusinessKeyIdentity, "loan_no", "installment_no"),
		maintenance("cbr_collateral", "CBR Collateral", MaintenanceCBR, "cbrcollateral.csv", "fincloud_cbr_collateral", RowNumberIdentity),
		maintenance("cbr_customer", "CBR Customer", MaintenanceCBR, "cbrcustomer.csv", "fincloud_cbr_customer", RowNumberIdentity),
		maintenance("cbr_loan", "CBR Loan", MaintenanceCBR, "cbrloan.csv", "fincloud_cbr_loan", BusinessKeyIdentity, "loan_no"),
		maintenance("cbr_savings", "CBR Savings", MaintenanceCBR, "cbrsavings.csv", "fincloud_cbr_savings", BusinessKeyIdentity, "acc_no"),
		maintenance("cbr_time_deposit", "CBR Time Deposit", MaintenanceCBR, "cbrtimedeposit.csv", "fincloud_cbr_time_deposit", BusinessKeyIdentity, "acc_no"),
	}
}

func maintenance(key, name string, kind MaintenanceKind, filename, table string, identity MaintenanceIdentity, businessKeys ...string) MaintenanceDefinition {
	return MaintenanceDefinition{Key: key, Name: name, Kind: kind, FilePattern: regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(filename) + `$`), TableName: table, Identity: identity, BusinessKeyColumns: append([]string(nil), businessKeys...), SchemaMode: DynamicAdditive}
}

func fixtureGap(definition MaintenanceDefinition) MaintenanceDefinition {
	definition.FixtureGapAccepted = true
	return definition
}

func ClassifyMaintenanceFile(kind MaintenanceKind, filename string) (MaintenanceDisposition, *MaintenanceDefinition) {
	base := filepath.Base(filename)
	if kind == MaintenanceCBR && regexp.MustCompile(`(?i)^Done\.csv$`).MatchString(base) {
		return MaintenanceDone, nil
	}
	if kind == MaintenanceCBR && regexp.MustCompile(`(?i)^SIPENDAR[0-9]{8}\.csv$`).MatchString(base) {
		return MaintenanceSIPENDAR, nil
	}
	for _, definition := range MaintenanceDefinitions() {
		if definition.Kind == kind && definition.FilePattern.MatchString(base) {
			copy := definition
			return MaintenanceRegistered, &copy
		}
	}
	return MaintenanceUnknown, nil
}

func MaintenanceCandidateDates(parameters MaintenanceParams) ([]CalendarDate, error) {
	if parameters.RequestedDate.IsZero() || parameters.LookbackDays < 0 || parameters.LookbackDays > 3 {
		return nil, fmt.Errorf("maintenance requested date and lookback between 0 and 3 are required")
	}
	dates := make([]CalendarDate, parameters.LookbackDays+1)
	for offset := range dates {
		dates[offset] = parameters.RequestedDate.AddDays(-offset)
	}
	return dates, nil
}

type MaintenanceFile struct {
	SourcePath string
	FileName   string
	Content    string
}

type MaintenanceResolution struct {
	SourcePath  string
	FileName    string
	Disposition MaintenanceDisposition
	Key         string
}

type MaintenanceBundle struct {
	Kind        MaintenanceKind
	AsOfDate    CalendarDate
	Files       map[string]MaintenanceFile
	Resolutions []MaintenanceResolution
}

func ResolveMaintenanceBundle(kind MaintenanceKind, asOfDate CalendarDate, sourceFiles map[string]string) (MaintenanceBundle, error) {
	if kind != MaintenanceEOD && kind != MaintenanceCBR {
		return MaintenanceBundle{}, fmt.Errorf("unsupported maintenance kind %q", kind)
	}
	if asOfDate.IsZero() {
		return MaintenanceBundle{}, fmt.Errorf("maintenance as_of_date is required")
	}
	if err := validateMaintenanceDefinitions(MaintenanceDefinitions()); err != nil {
		return MaintenanceBundle{}, err
	}
	bundle := MaintenanceBundle{Kind: kind, AsOfDate: asOfDate, Files: make(map[string]MaintenanceFile)}
	paths := make([]string, 0, len(sourceFiles))
	for sourcePath := range sourceFiles {
		paths = append(paths, sourcePath)
	}
	sort.Strings(paths)
	for _, sourcePath := range paths {
		filename := filepath.Base(sourcePath)
		disposition, definition := ClassifyMaintenanceFile(kind, filename)
		resolution := MaintenanceResolution{SourcePath: sourcePath, FileName: filename, Disposition: disposition}
		if definition != nil {
			resolution.Key = definition.Key
			if previous, duplicate := bundle.Files[definition.Key]; duplicate {
				return MaintenanceBundle{}, fmt.Errorf("duplicate maintenance source %s: %s and %s", definition.Key, previous.SourcePath, sourcePath)
			}
			bundle.Files[definition.Key] = MaintenanceFile{SourcePath: sourcePath, FileName: filename, Content: sourceFiles[sourcePath]}
		}
		bundle.Resolutions = append(bundle.Resolutions, resolution)
	}
	return bundle, nil
}

type MaintenanceColumn struct {
	OriginalHeader string
	PhysicalName   string
	Ordinal        int
}

type MaintenanceRow struct {
	SourceRowNumber int
	BusinessKeyHash string
	RowChecksum     string
	Values          []string
}

type ParsedMaintenanceCSV struct {
	Definition MaintenanceDefinition
	AsOfDate   CalendarDate
	Columns    []MaintenanceColumn
	Rows       []MaintenanceRow
}

var reservedMaintenanceColumns = map[string]struct{}{
	"as_of_date": {}, "business_key_hash": {}, "source_file_name": {}, "source_row_number": {},
	"source_row_checksum": {}, "ingestion_run_id": {}, "created_at": {}, "updated_at": {},
}

func NormalizeMaintenanceHeaders(headers []string) ([]MaintenanceColumn, error) {
	if len(headers) == 0 {
		return nil, fmt.Errorf("empty CSV header")
	}
	columns := make([]MaintenanceColumn, len(headers))
	seenOriginal, seenPhysical := map[string]struct{}{}, map[string]string{}
	for index, original := range headers {
		if index == 0 {
			original = strings.TrimPrefix(original, "\uFEFF")
		}
		if strings.TrimSpace(original) == "" {
			return nil, fmt.Errorf("empty CSV header at position %d", index+1)
		}
		if _, exists := seenOriginal[original]; exists {
			return nil, fmt.Errorf("duplicate CSV header %q", original)
		}
		seenOriginal[original] = struct{}{}
		physical := toSnakeCase(original)
		if physical == "" {
			return nil, fmt.Errorf("CSV header %q normalizes to empty identifier", original)
		}
		if physical[0] >= '0' && physical[0] <= '9' {
			physical = "col_" + physical
		}
		if len(physical) > maxMySQLIdentifierLength {
			hash := checksum([]byte(physical))
			physical = physical[:51] + "_" + hash[:12]
		}
		if !regexp.MustCompile(`^[a-z_][a-z0-9_]*$`).MatchString(physical) {
			return nil, fmt.Errorf("CSV header %q normalizes to unsafe identifier %q", original, physical)
		}
		if _, reserved := reservedMaintenanceColumns[physical]; reserved {
			return nil, fmt.Errorf("CSV header %q conflicts with reserved metadata column %q", original, physical)
		}
		if previous, collision := seenPhysical[physical]; collision {
			return nil, fmt.Errorf("CSV headers %q and %q normalize to duplicate column %q", previous, original, physical)
		}
		seenPhysical[physical] = original
		columns[index] = MaintenanceColumn{OriginalHeader: original, PhysicalName: physical, Ordinal: index + 1}
	}
	return columns, nil
}

func ParseMaintenanceCSV(ctx context.Context, definition MaintenanceDefinition, asOfDate CalendarDate, content string) (ParsedMaintenanceCSV, error) {
	reader := csv.NewReader(strings.NewReader(content))
	reader.Comma = '|'
	headers, err := reader.Read()
	if err == io.EOF {
		return ParsedMaintenanceCSV{}, fmt.Errorf("%s: no CSV header", definition.Key)
	}
	if err != nil {
		return ParsedMaintenanceCSV{}, err
	}
	columns, err := NormalizeMaintenanceHeaders(headers)
	if err != nil {
		return ParsedMaintenanceCSV{}, err
	}
	indexes := make(map[string]int, len(columns))
	for index, column := range columns {
		indexes[column.PhysicalName] = index
	}
	keyIndexes := make([]int, len(definition.BusinessKeyColumns))
	for index, key := range definition.BusinessKeyColumns {
		value, exists := indexes[key]
		if !exists {
			return ParsedMaintenanceCSV{}, fmt.Errorf("%s: missing business-key column %q", definition.Key, key)
		}
		keyIndexes[index] = value
	}
	var rows []MaintenanceRow
	seenKeys := map[string]int{}
	for {
		if err := ctx.Err(); err != nil {
			return ParsedMaintenanceCSV{}, err
		}
		values, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return ParsedMaintenanceCSV{}, readErr
		}
		line, _ := reader.FieldPos(0)
		row := MaintenanceRow{SourceRowNumber: line, Values: append([]string(nil), values...), RowChecksum: maintenanceRowChecksum(columns, values)}
		if definition.Identity == BusinessKeyIdentity {
			parts := make([]string, len(keyIndexes))
			for index, keyIndex := range keyIndexes {
				parts[index] = strings.TrimSpace(values[keyIndex])
				if parts[index] == "" {
					return ParsedMaintenanceCSV{}, fmt.Errorf("%s row %d: blank business-key column %q", definition.Key, line, definition.BusinessKeyColumns[index])
				}
			}
			encoded, _ := json.Marshal(parts)
			row.BusinessKeyHash = checksum(encoded)
			if first, duplicate := seenKeys[row.BusinessKeyHash]; duplicate {
				return ParsedMaintenanceCSV{}, fmt.Errorf("%s: duplicate business key at rows %d and %d", definition.Key, first, line)
			}
			seenKeys[row.BusinessKeyHash] = line
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return ParsedMaintenanceCSV{}, fmt.Errorf("%s: empty snapshot is not allowed", definition.Key)
	}
	return ParsedMaintenanceCSV{Definition: definition, AsOfDate: asOfDate, Columns: columns, Rows: rows}, nil
}

func maintenanceRowChecksum(columns []MaintenanceColumn, values []string) string {
	type field struct{ Name, Value string }
	fields := make([]field, len(columns))
	for index, column := range columns {
		fields[index] = field{Name: column.PhysicalName, Value: values[index]}
	}
	sort.Slice(fields, func(left, right int) bool { return fields[left].Name < fields[right].Name })
	encoded, _ := json.Marshal(fields)
	return checksum(encoded)
}

func checksum(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }

func toSnakeCase(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	var builder strings.Builder
	previousSeparator := false
	for index, current := range runes {
		if current == '_' || current == '-' || current == '.' || current == '/' || unicode.IsSpace(current) || (!unicode.IsLetter(current) && !unicode.IsDigit(current)) {
			if builder.Len() > 0 && !previousSeparator {
				builder.WriteByte('_')
				previousSeparator = true
			}
			continue
		}
		if unicode.IsUpper(current) && index > 0 && !previousSeparator {
			previous := runes[index-1]
			if unicode.IsLower(previous) || unicode.IsDigit(previous) || (unicode.IsUpper(previous) && index+1 < len(runes) && unicode.IsLower(runes[index+1])) {
				builder.WriteByte('_')
			}
		}
		builder.WriteRune(unicode.ToLower(current))
		previousSeparator = false
	}
	return strings.Trim(builder.String(), "_")
}
