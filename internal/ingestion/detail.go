package ingestion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ibldzn/go-admin/internal/fincloud"
)

type DetailDomain string

const (
	DetailCIF         DetailDomain = "cif"
	DetailSaving      DetailDomain = "saving"
	DetailTimeDeposit DetailDomain = "time_deposit"
	DetailLoan        DetailDomain = "loan"
)

type MapperMetadata struct {
	class, field, category, reason, safeMessage string
}

func (metadata MapperMetadata) Class() string       { return metadata.class }
func (metadata MapperMetadata) Field() string       { return metadata.field }
func (metadata MapperMetadata) Category() string    { return metadata.category }
func (metadata MapperMetadata) Reason() string      { return metadata.reason }
func (metadata MapperMetadata) SafeMessage() string { return metadata.safeMessage }

func IsSafeMapperDiagnostic(class, field, category, reason, message string) bool {
	if class != "detail_mapping" || !mapperFieldAllowed(field) {
		return false
	}
	if _, found := map[string]struct{}{"string": {}, "decimal": {}, "integer": {}, "date": {}, "datetime": {}, "identifier": {}, "structure": {}}[category]; !found {
		return false
	}
	if _, found := map[string]struct{}{"missing": {}, "required": {}, "invalid_json": {}, "expected_array": {}, "expected_object": {}, "invalid_value": {}}[reason]; !found {
		return false
	}
	_, found := map[string]struct{}{
		"detail payload is missing": {}, "detail payload is invalid": {}, "detail identifier is missing": {},
		"required detail field is missing": {}, "detail field value is invalid": {},
		"detail child collection is invalid": {}, "detail child item is invalid": {},
	}[message]
	return found
}

func mapperFieldAllowed(field string) bool {
	if field == "payload" || field == "identifier" {
		return true
	}
	for _, domain := range []DetailDomain{DetailCIF, DetailSaving, DetailTimeDeposit, DetailLoan} {
		shape, _ := detailShapeFor(domain)
		for _, candidate := range shape.fields {
			if field == candidate.target {
				return true
			}
		}
		for _, child := range shape.children {
			if field == child.sourceKey {
				return true
			}
			for _, candidate := range child.fields {
				if field == child.sourceKey+"."+candidate.target {
					return true
				}
			}
		}
	}
	return false
}

type MapperError struct {
	metadata MapperMetadata
	cause    error
}

func (err *MapperError) Error() string {
	if err == nil {
		return "detail mapping failed"
	}
	return err.metadata.safeMessage
}

func (err *MapperError) Unwrap() error { return err.cause }

func (err *MapperError) Metadata() MapperMetadata {
	if err == nil {
		return MapperMetadata{}
	}
	return err.metadata
}

func mapperError(field, category, reason, message string, cause error) error {
	return &MapperError{metadata: MapperMetadata{
		class: "detail_mapping", field: field, category: category, reason: reason, safeMessage: message,
	}, cause: cause}
}

type DetailRecord struct {
	Domain        DetailDomain
	Identifier    string
	LastFetchedAt time.Time
	Fields        map[string]any
	RawPayload    json.RawMessage
	RawChecksum   string
	Children      map[string][]DetailChildRecord
}

type DetailChildRecord struct {
	Identifier      string
	ItemIndex       int
	Fields          map[string]any
	RawItemPayload  json.RawMessage
	RawItemChecksum string
}

func MapCIFDetail(ctx context.Context, detail *fincloud.CIFDetail, fetchedAt time.Time) (DetailRecord, error) {
	if detail == nil {
		return DetailRecord{}, mapperError("payload", "structure", "missing", "detail payload is missing", nil)
	}
	return MapDetailPayload(ctx, DetailCIF, detail.RawPayload, fetchedAt)
}

func MapSavingDetail(ctx context.Context, detail *fincloud.SavingDetail, fetchedAt time.Time) (DetailRecord, error) {
	if detail == nil {
		return DetailRecord{}, mapperError("payload", "structure", "missing", "detail payload is missing", nil)
	}
	return MapDetailPayload(ctx, DetailSaving, detail.RawPayload, fetchedAt)
}

func MapTimeDepositDetail(ctx context.Context, detail *fincloud.TimeDepositDetail, fetchedAt time.Time) (DetailRecord, error) {
	if detail == nil {
		return DetailRecord{}, mapperError("payload", "structure", "missing", "detail payload is missing", nil)
	}
	return MapDetailPayload(ctx, DetailTimeDeposit, detail.RawPayload, fetchedAt)
}

func MapLoanDetail(ctx context.Context, detail *fincloud.LoanDetail, fetchedAt time.Time) (DetailRecord, error) {
	if detail == nil {
		return DetailRecord{}, mapperError("payload", "structure", "missing", "detail payload is missing", nil)
	}
	return MapDetailPayload(ctx, DetailLoan, detail.RawPayload, fetchedAt)
}

func MapDetailPayload(ctx context.Context, domain DetailDomain, raw json.RawMessage, fetchedAt time.Time) (DetailRecord, error) {
	if fetchedAt.IsZero() {
		return DetailRecord{}, fmt.Errorf("last_fetched_at is required")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return DetailRecord{}, mapperError("payload", "structure", "missing", "detail payload is missing", nil)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return DetailRecord{}, mapperError("payload", "structure", "invalid_json", "detail payload is invalid", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(compact.Bytes()))
	decoder.UseNumber()
	fields := make(map[string]any)
	if err := decoder.Decode(&fields); err != nil {
		return DetailRecord{}, mapperError("payload", "structure", "invalid_json", "detail payload is invalid", err)
	}
	shape, err := detailShapeFor(domain)
	if err != nil {
		return DetailRecord{}, err
	}
	identifier, ok := fields[shape.identifierKey].(string)
	if !ok || strings.TrimSpace(identifier) == "" {
		return DetailRecord{}, mapperError(identifierField(shape), "identifier", "required", "detail identifier is missing", nil)
	}
	mappedFields, err := mapFields(fields, shape.fields, "")
	if err != nil {
		return DetailRecord{}, fmt.Errorf("%s detail: %w", domain, err)
	}
	payload := append(json.RawMessage(nil), compact.Bytes()...)
	record := DetailRecord{Domain: domain, Identifier: identifier, LastFetchedAt: fetchedAt, Fields: mappedFields, RawPayload: payload, RawChecksum: sha256Hex(payload), Children: make(map[string][]DetailChildRecord)}
	for _, childShape := range shape.children {
		if err := ctx.Err(); err != nil {
			return DetailRecord{}, err
		}
		rawChildren, exists := fields[childShape.sourceKey]
		if !exists || rawChildren == nil {
			continue
		}
		children, ok := rawChildren.([]any)
		if !ok {
			return DetailRecord{}, mapperError(childShape.sourceKey, "structure", "expected_array", "detail child collection is invalid", nil)
		}
		mapped := make([]DetailChildRecord, len(children))
		for index, child := range children {
			if err := ctx.Err(); err != nil {
				return DetailRecord{}, err
			}
			object, ok := child.(map[string]any)
			if !ok {
				return DetailRecord{}, mapperError(childShape.sourceKey, "structure", "expected_object", "detail child item is invalid", nil)
			}
			data, err := json.Marshal(object)
			if err != nil {
				return DetailRecord{}, mapperError(childShape.sourceKey, "structure", "invalid_json", "detail child item is invalid", err)
			}
			mappedFields, err := mapFields(object, childShape.fields, childShape.sourceKey+".")
			if err != nil {
				return DetailRecord{}, fmt.Errorf("%s[%d]: %w", childShape.sourceKey, index, err)
			}
			mapped[index] = DetailChildRecord{Identifier: identifier, ItemIndex: index, Fields: mappedFields, RawItemPayload: data, RawItemChecksum: sha256Hex(data)}
		}
		record.Children[childShape.sourceKey] = mapped
	}
	return record, nil
}

type detailValueType int

const (
	detailString detailValueType = iota
	detailDecimal
	detailInteger
	detailDate
	detailDateTime
)

type detailField struct {
	target, source string
	valueType      detailValueType
	required       bool
}
type detailChildShape struct {
	sourceKey string
	fields    []detailField
}
type detailShape struct {
	identifierKey string
	fields        []detailField
	children      []detailChildShape
}

func detailShapeFor(domain DetailDomain) (detailShape, error) {
	switch domain {
	case DetailCIF:
		return detailShape{identifierKey: "id", fields: []detailField{
			{"cif_no", "id", detailString, true}, {"customer_name", "namanasabah", detailString, true},
			{"customer_type", "jenisnasabah", detailString, false}, {"identity_type", "jenisidentitas", detailString, false},
			{"ktp_no", "perorangan_noktp", detailString, false}, {"birth_date", "perorangan_tgllahir", detailDate, false},
			{"cif_open_date", "tglbukacif", detailDate, false}, {"record_created_at", "rec_dibuat_tgljam", detailDateTime, false},
		}}, nil
	case DetailSaving:
		return detailShape{identifierKey: "norekening", fields: []detailField{
			{"account_no", "norekening", detailString, true}, {"cif_no", "nocif", detailString, true},
			{"account_name", "nama", detailString, false}, {"location_id", "locationid", detailString, false},
			{"beginning_balance", "saldoawal", detailDecimal, true}, {"balance", "saldoakhir", detailDecimal, true},
			{"blocked_balance", "saldoblokir", detailDecimal, false}, {"debit_mutation", "mutasidebit", detailDecimal, false},
			{"credit_mutation", "mutasikredit", detailDecimal, false}, {"open_date", "tglbukarekening", detailDate, false},
			{"closed_date", "tglclosed", detailDate, false},
		}}, nil
	case DetailTimeDeposit:
		return detailShape{identifierKey: "id", fields: []detailField{
			{"account_no", "id", detailString, true}, {"cif_no", "nocif", detailString, true},
			{"nominal", "nominal", detailDecimal, true}, {"accrued_interest", "accrueinterest", detailDecimal, false},
			{"product_interest_rate", "produk_sukubunga", detailDecimal, false}, {"open_date", "tglbukarekening", detailDate, false},
			{"maturity_date", "tglpencairan", detailDate, false}, {"location_id", "locationid", detailString, false},
		}, children: []detailChildShape{{sourceKey: "mutasideposito", fields: []detailField{
			{"transaction_date", "tgltransaksi", detailDate, false}, {"transaction_type", "jenistransaksi", detailString, false},
			{"currency", "currency", detailString, false}, {"nominal", "nominal", detailDecimal, false},
			{"interest_rate", "sukubunga", detailDecimal, false}, {"reference", "referensi", detailString, false},
			{"branch", "cabang", detailString, false}, {"journal_no", "nojurnal", detailString, false},
		}}}}, nil
	case DetailLoan:
		return detailShape{identifierKey: "id", fields: []detailField{
			{"account_no", "id", detailString, true}, {"cif_no", "nocif", detailString, true},
			{"location_id", "locationid", detailString, false}, {"disbursement_date", "tgl_pencairan", detailDate, false},
			{"outstanding_principal", "outstandingpinjaman", detailDecimal, false}, {"principal_arrears", "tunggakanpokok", detailDecimal, false},
			{"interest_arrears", "tunggakanbunga", detailDecimal, false}, {"penalty_arrears", "dendatunggakan", detailDecimal, false},
			{"dpd", "dpd", detailInteger, false}, {"collectability_bi", "kolekbi", detailInteger, false},
			{"product_interest_rate", "produk_sukubunga", detailDecimal, false}, {"write_off_date", "tglhapusbuku", detailDate, false},
		}, children: []detailChildShape{
			{sourceKey: "biayapencairan", fields: []detailField{{"fee_name", "namabiaya", detailString, false}, {"fee_amount", "jumlah_biaya", detailDecimal, false}, {"calculate_dwp", "hitungdwp", detailString, false}}},
			{sourceKey: "jadwalangsuran", fields: []detailField{{"schedule_date", "tanggal", detailDate, false}, {"installment_amount", "angsuran", detailDecimal, false}, {"interest_amount", "bunga", detailDecimal, false}, {"principal_amount", "pokok", detailDecimal, false}, {"penalty_amount", "denda", detailDecimal, false}, {"paid_principal", "bayar_pokok", detailDecimal, false}, {"paid_interest", "bayar_bunga", detailDecimal, false}, {"paid_penalty", "bayar_denda", detailDecimal, false}, {"remaining_loan", "sisapinjaman", detailDecimal, false}, {"installment_no", "angsuranke", detailInteger, false}}},
			{sourceKey: "historybayar", fields: []detailField{{"transaction_date", "tgl", detailDate, false}, {"installment_no", "angsuranke", detailInteger, false}, {"payment_date", "tglbayar", detailDate, false}, {"currency", "currency", detailString, false}, {"due_date", "tgljt", detailDate, false}, {"total_paid", "totalbayar", detailDecimal, false}, {"paid_principal", "bayar_pokok", detailDecimal, false}, {"paid_interest", "bayar_bunga", detailDecimal, false}, {"paid_penalty", "bayar_denda", detailDecimal, false}, {"journal_no", "nojurnal", detailString, false}, {"branch", "cabang", detailString, false}}},
		}}, nil
	default:
		return detailShape{}, fmt.Errorf("unsupported detail domain %q", domain)
	}
}

func mapFields(source map[string]any, specifications []detailField, prefix string) (map[string]any, error) {
	mapped := make(map[string]any, len(specifications))
	for _, specification := range specifications {
		value, exists := source[specification.source]
		valueString, isString := value.(string)
		if !exists || value == nil || (isString && strings.TrimSpace(valueString) == "") {
			if specification.required {
				return nil, mapperError(prefix+specification.target, specification.valueType.category(), "required", "required detail field is missing", nil)
			}
			mapped[specification.target] = nil
			continue
		}
		converted, err := convertDetailValue(value, specification.valueType)
		if err != nil {
			return nil, mapperError(prefix+specification.target, specification.valueType.category(), "invalid_value", "detail field value is invalid", err)
		}
		mapped[specification.target] = converted
	}
	return mapped, nil
}

func (valueType detailValueType) category() string {
	switch valueType {
	case detailString:
		return "string"
	case detailDecimal:
		return "decimal"
	case detailInteger:
		return "integer"
	case detailDate:
		return "date"
	case detailDateTime:
		return "datetime"
	default:
		return "structure"
	}
}

func identifierField(shape detailShape) string {
	for _, field := range shape.fields {
		if field.source == shape.identifierKey {
			return field.target
		}
	}
	return "identifier"
}

func convertDetailValue(value any, valueType detailValueType) (any, error) {
	scalar := func() (string, error) {
		switch typed := value.(type) {
		case string:
			return strings.TrimSpace(typed), nil
		case json.Number:
			return typed.String(), nil
		case bool:
			return strconv.FormatBool(typed), nil
		default:
			return "", fmt.Errorf("must be scalar")
		}
	}
	switch valueType {
	case detailString:
		return scalar()
	case detailDecimal:
		text, err := scalar()
		if err != nil {
			return nil, err
		}
		return fincloud.Scalar(text).Decimal()
	case detailInteger:
		text, err := scalar()
		if err != nil {
			return nil, err
		}
		return strconv.ParseInt(text, 10, 64)
	case detailDate:
		if wrapper, ok := value.(map[string]any); ok {
			wrapped, ok := wrapper["date"].(string)
			if !ok {
				return nil, fmt.Errorf("date wrapper missing date")
			}
			return ParseFincloudDate(wrapped)
		}
		text, err := scalar()
		if err != nil {
			return nil, err
		}
		return ParseFincloudDate(text)
	case detailDateTime:
		if wrapper, ok := value.(map[string]any); ok {
			wrapped, ok := wrapper["date"].(string)
			if !ok {
				return nil, fmt.Errorf("datetime wrapper missing date")
			}
			if wrapperType, _ := wrapper["type"].(string); wrapperType != "timestamp" {
				return nil, fmt.Errorf("datetime wrapper type %q is not timestamp", wrapperType)
			}
			return ParseFincloudDateTime(wrapped)
		}
		text, err := scalar()
		if err != nil {
			return nil, err
		}
		return ParseFincloudDateTime(text)
	default:
		return nil, fmt.Errorf("unsupported detail value type")
	}
}

func ParseFincloudDate(value string) (CalendarDate, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05", "2006-01-02 15:04:05.999999", time.RFC3339Nano} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return CalendarDateFromTime(parsed), nil
		}
	}
	return CalendarDate{}, fmt.Errorf("invalid Fincloud date %q", value)
}

func ParseWrappedFincloudDate(value *fincloud.WrappedDateTime) (CalendarDate, error) {
	if value == nil || strings.TrimSpace(value.Date) == "" {
		return CalendarDate{}, fmt.Errorf("Fincloud date wrapper is empty")
	}
	return ParseFincloudDate(value.Date)
}

func ParseFincloudDateTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02 15:04:05.999999", "2006-01-02 15:04:05", time.RFC3339Nano} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid Fincloud datetime")
}

var longDigitRun = regexp.MustCompile(`[0-9]{6,}`)

func MaskLongDigitRuns(value string) string {
	return longDigitRun.ReplaceAllStringFunc(value, func(match string) string {
		if len(match) <= 8 {
			return strings.Repeat("*", len(match))
		}
		return match[:4] + strings.Repeat("*", len(match)-8) + match[len(match)-4:]
	})
}

func sha256Hex(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
