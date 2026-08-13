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
	"github.com/shopspring/decimal"
)

type DetailDomain string

const (
	DetailCIF         DetailDomain = "cif"
	DetailSaving      DetailDomain = "saving"
	DetailTimeDeposit DetailDomain = "time_deposit"
	DetailLoan        DetailDomain = "loan"
)

type DetailRecord struct {
	Domain        DetailDomain
	Identifier    string
	AsOfDate      CalendarDate
	LastFetchedAt time.Time
	Fields        map[string]any
	RawPayload    json.RawMessage
	RawChecksum   string
	Children      map[string][]DetailChildRecord
}

type DetailChildRecord struct {
	Identifier      string
	AsOfDate        CalendarDate
	ItemIndex       int
	Fields          map[string]any
	RawItemPayload  json.RawMessage
	RawItemChecksum string
}

func MapCIFDetail(ctx context.Context, detail *fincloud.CIFDetail, asOfDate CalendarDate, fetchedAt time.Time) (DetailRecord, error) {
	if detail == nil {
		return DetailRecord{}, fmt.Errorf("CIF detail is required")
	}
	return MapDetailPayload(ctx, DetailCIF, detail.RawPayload, asOfDate, fetchedAt)
}

func MapSavingDetail(ctx context.Context, detail *fincloud.SavingDetail, asOfDate CalendarDate, fetchedAt time.Time) (DetailRecord, error) {
	if detail == nil {
		return DetailRecord{}, fmt.Errorf("saving detail is required")
	}
	return MapDetailPayload(ctx, DetailSaving, detail.RawPayload, asOfDate, fetchedAt)
}

func MapTimeDepositDetail(ctx context.Context, detail *fincloud.TimeDepositDetail, asOfDate CalendarDate, fetchedAt time.Time) (DetailRecord, error) {
	if detail == nil {
		return DetailRecord{}, fmt.Errorf("time-deposit detail is required")
	}
	return MapDetailPayload(ctx, DetailTimeDeposit, detail.RawPayload, asOfDate, fetchedAt)
}

func MapLoanDetail(ctx context.Context, detail *fincloud.LoanDetail, asOfDate CalendarDate, fetchedAt time.Time) (DetailRecord, error) {
	if detail == nil {
		return DetailRecord{}, fmt.Errorf("loan detail is required")
	}
	return MapDetailPayload(ctx, DetailLoan, detail.RawPayload, asOfDate, fetchedAt)
}

func MapDetailPayload(ctx context.Context, domain DetailDomain, raw json.RawMessage, asOfDate CalendarDate, fetchedAt time.Time) (DetailRecord, error) {
	if asOfDate.IsZero() {
		return DetailRecord{}, fmt.Errorf("as_of_date is required")
	}
	if fetchedAt.IsZero() {
		return DetailRecord{}, fmt.Errorf("last_fetched_at is required")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return DetailRecord{}, fmt.Errorf("raw detail payload is required")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return DetailRecord{}, fmt.Errorf("compact raw detail payload: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(compact.Bytes()))
	decoder.UseNumber()
	fields := make(map[string]any)
	if err := decoder.Decode(&fields); err != nil {
		return DetailRecord{}, fmt.Errorf("decode raw detail payload: %w", err)
	}
	shape, err := detailShapeFor(domain)
	if err != nil {
		return DetailRecord{}, err
	}
	identifier, ok := fields[shape.identifierKey].(string)
	if !ok || strings.TrimSpace(identifier) == "" {
		return DetailRecord{}, fmt.Errorf("%s detail identifier %q is required", domain, shape.identifierKey)
	}
	mappedFields, err := mapFields(fields, shape.fields)
	if err != nil {
		return DetailRecord{}, fmt.Errorf("%s detail: %w", domain, err)
	}
	payload := append(json.RawMessage(nil), compact.Bytes()...)
	record := DetailRecord{Domain: domain, Identifier: identifier, AsOfDate: asOfDate, LastFetchedAt: fetchedAt, Fields: mappedFields, RawPayload: payload, RawChecksum: sha256Hex(payload), Children: make(map[string][]DetailChildRecord)}
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
			return DetailRecord{}, fmt.Errorf("%s must be an array", childShape.sourceKey)
		}
		mapped := make([]DetailChildRecord, len(children))
		for index, child := range children {
			if err := ctx.Err(); err != nil {
				return DetailRecord{}, err
			}
			object, ok := child.(map[string]any)
			if !ok {
				return DetailRecord{}, fmt.Errorf("%s[%d] must be an object", childShape.sourceKey, index)
			}
			data, err := json.Marshal(object)
			if err != nil {
				return DetailRecord{}, err
			}
			mappedFields, err := mapFields(object, childShape.fields)
			if err != nil {
				return DetailRecord{}, fmt.Errorf("%s[%d]: %w", childShape.sourceKey, index, err)
			}
			mapped[index] = DetailChildRecord{Identifier: identifier, AsOfDate: asOfDate, ItemIndex: index, Fields: mappedFields, RawItemPayload: data, RawItemChecksum: sha256Hex(data)}
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

func mapFields(source map[string]any, specifications []detailField) (map[string]any, error) {
	mapped := make(map[string]any, len(specifications))
	for _, specification := range specifications {
		value, exists := source[specification.source]
		valueString, isString := value.(string)
		if !exists || value == nil || (isString && strings.TrimSpace(valueString) == "") {
			if specification.required {
				return nil, fmt.Errorf("%s is required", specification.target)
			}
			mapped[specification.target] = nil
			continue
		}
		converted, err := convertDetailValue(value, specification.valueType)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", specification.target, err)
		}
		mapped[specification.target] = converted
	}
	return mapped, nil
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
		return decimal.NewFromString(text)
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
