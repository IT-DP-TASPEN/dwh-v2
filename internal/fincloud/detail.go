package fincloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type Scalar string

func (scalar *Scalar) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*scalar = ""
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	switch typed := value.(type) {
	case string:
		*scalar = Scalar(typed)
	case json.Number:
		*scalar = Scalar(typed.String())
	case bool:
		*scalar = Scalar(strconv.FormatBool(typed))
	default:
		return fmt.Errorf("unsupported Fincloud scalar JSON value")
	}
	return nil
}

func (scalar Scalar) String() string { return string(scalar) }

func (scalar Scalar) Decimal() (decimal.Decimal, error) {
	text := strings.TrimSpace(string(scalar))
	if value, err := decimal.NewFromString(text); err == nil {
		return value, nil
	}
	normalized, err := normalizeGroupedDecimal(text)
	if err != nil {
		return decimal.Decimal{}, err
	}
	return decimal.NewFromString(normalized)
}

// normalizeGroupedDecimal accepts Fincloud's observed comma-grouped numbers
// only after validating every thousands group. A lone separator followed by
// three digits is rejected because it is ambiguous with a decimal separator.
func normalizeGroupedDecimal(value string) (string, error) {
	original := value
	if value == "" {
		return "", fmt.Errorf("decimal value is required")
	}
	sign := ""
	if value[0] == '+' || value[0] == '-' {
		sign, value = value[:1], value[1:]
		if value == "" {
			return "", fmt.Errorf("invalid grouped decimal %q", original)
		}
	}
	if strings.Count(value, ".") > 1 {
		return "", fmt.Errorf("invalid grouped decimal %q", original)
	}
	integer, fraction, hasFraction := value, "", false
	if dot := strings.IndexByte(value, '.'); dot >= 0 {
		integer, fraction, hasFraction = value[:dot], value[dot+1:], true
		if fraction == "" || !asciiDigits(fraction) {
			return "", fmt.Errorf("invalid grouped decimal %q", original)
		}
	}
	groups := strings.Split(integer, ",")
	if len(groups) < 2 || len(groups[0]) < 1 || len(groups[0]) > 3 || !asciiDigits(groups[0]) {
		return "", fmt.Errorf("invalid grouped decimal %q", original)
	}
	for _, group := range groups[1:] {
		if len(group) != 3 || !asciiDigits(group) {
			return "", fmt.Errorf("invalid grouped decimal %q", original)
		}
	}
	if !hasFraction && len(groups) == 2 {
		return "", fmt.Errorf("ambiguous grouped decimal %q", original)
	}
	normalized := sign + strings.Join(groups, "")
	if hasFraction {
		normalized += "." + fraction
	}
	return normalized, nil
}

func asciiDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range []byte(value) {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

type WrappedDateTime struct {
	Date         string `json:"date"`
	Timezone     string `json:"timezone,omitempty"`
	TimezoneType int    `json:"timezone_type,omitempty"`
	Type         string `json:"type,omitempty"`
}

type DetailEnvelope[T any] struct {
	Status string `json:"status"`
	Data   struct {
		Result T `json:"result"`
	} `json:"data"`
}

type CIFDetail struct {
	RawPayload   json.RawMessage  `json:"-"`
	ID           string           `json:"id"`
	CustomerName string           `json:"namanasabah"`
	AltNo        string           `json:"noalt"`
	OpenDate     *WrappedDateTime `json:"tglbukacif"`
	CreatedAt    *WrappedDateTime `json:"rec_dibuat_tgljam"`
	UpdatedAt    *WrappedDateTime `json:"rec_diupdate_tgljam"`
	Savings      []CIFSaving      `json:"tabungan"`
}

type CIFSaving struct {
	ID             string `json:"id"`
	Name           string `json:"nama"`
	Currency       string `json:"currency"`
	DocumentStatus string `json:"status_dokumen"`
	OpenDate       string `json:"tglbukarekening"`
}

type SavingDetail struct {
	RawPayload       json.RawMessage `json:"-"`
	AccountNo        string          `json:"norekening"`
	CIFNo            string          `json:"nocif"`
	CustomerName     string          `json:"namanasabah"`
	Balance          Scalar          `json:"saldoakhir"`
	BeginningBalance Scalar          `json:"saldoawal"`
	DebitMutation    Scalar          `json:"mutasidebit"`
	CreditMutation   Scalar          `json:"mutasikredit"`
}

type SavingAccountStatement struct {
	Mutations []SavingAccountStatementItem
}

type SavingAccountStatementItem struct {
	RawPayload               json.RawMessage `json:"-"`
	TransactionDate          *string         `json:"tgltransaksi"`
	TransactionTime          *string         `json:"jam"`
	OpeningBalance           *string         `json:"saldoawal"`
	Debit                    *string         `json:"debit"`
	Credit                   *string         `json:"kredit"`
	ClosingBalance           *string         `json:"saldoakhir"`
	ClosingBalanceEquivalent *string         `json:"saldoakhir_equivalent"`
	TransactionType          *string         `json:"jenistransaksi"`
	Description              *string         `json:"keterangan"`
	Reference                *string         `json:"referensi"`
	Location                 *string         `json:"lokasi"`
	JournalNo                *string         `json:"nojurnal"`
	CreatedBy                *string         `json:"rec_dibuat_oleh"`
	TransactionRate          *StrictNumber   `json:"trx_rate"`
	MidRateDC                *string         `json:"mid_rate_dc"`
}

type StrictNumber string

func (number *StrictNumber) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	parsed, ok := value.(json.Number)
	if !ok {
		return fmt.Errorf("Fincloud value must be a JSON number")
	}
	*number = StrictNumber(parsed.String())
	return nil
}

func (number StrictNumber) String() string { return string(number) }

type TimeDepositDetail struct {
	RawPayload      json.RawMessage       `json:"-"`
	ID              string                `json:"id"`
	CIFNo           string                `json:"nocif"`
	CustomerName    string                `json:"namanasabah"`
	Nominal         Scalar                `json:"nominal"`
	AccruedInterest Scalar                `json:"accrueinterest"`
	OpenDate        *WrappedDateTime      `json:"tglbukarekening"`
	MaturityDate    *WrappedDateTime      `json:"tglpencairan"`
	Mutations       []TimeDepositMutation `json:"mutasideposito"`
}

type TimeDepositMutation struct {
	Branch          string `json:"cabang"`
	Currency        string `json:"currency"`
	TransactionType string `json:"jenistransaksi"`
	Description     string `json:"keterangan"`
	JournalNo       string `json:"nojurnal"`
	Nominal         Scalar `json:"nominal"`
	InterestRate    Scalar `json:"sukubunga"`
	TransactionDate string `json:"tgltransaksi"`
}

type LoanDetail struct {
	RawPayload       json.RawMessage         `json:"-"`
	ID               string                  `json:"id"`
	CIFNo            string                  `json:"nocif"`
	CustomerName     string                  `json:"namanasabah"`
	PrincipalAmount  Scalar                  `json:"jmlpokok_pinjaman"`
	Outstanding      Scalar                  `json:"outstandingpinjaman"`
	DisbursementFees []LoanDisbursementFee   `json:"biayapencairan"`
	Repayment        []LoanRepaymentSchedule `json:"jadwalangsuran"`
	PaymentHistory   []LoanPaymentHistory    `json:"historybayar"`
}

type LoanDisbursementFee struct {
	Name         string `json:"namabiaya"`
	Amount       Scalar `json:"jumlah_biaya"`
	CalculateDWP string `json:"hitungdwp"`
}

type LoanRepaymentSchedule struct {
	InstallmentNo     Scalar `json:"angsuranke"`
	InstallmentAmount Scalar `json:"angsuran"`
	Principal         Scalar `json:"pokok"`
	Interest          Scalar `json:"bunga"`
	Penalty           Scalar `json:"denda"`
	PaidPrincipal     Scalar `json:"bayar_pokok"`
	PaidInterest      Scalar `json:"bayar_bunga"`
	PaidPenalty       Scalar `json:"bayar_denda"`
	RemainingLoan     Scalar `json:"sisapinjaman"`
	PaymentStatus     string `json:"statusbayar"`
	Date              string `json:"tanggal"`
}

type LoanPaymentHistory struct {
	TransactionDate WrappedDateTime `json:"tgl"`
	InstallmentNo   Scalar          `json:"angsuranke"`
	PaymentDate     string          `json:"tglbayar"`
	Currency        string          `json:"currency"`
	DueDate         string          `json:"tgljt"`
	TotalPaid       Scalar          `json:"totalbayar"`
	PaidPrincipal   Scalar          `json:"bayar_pokok"`
	PaidInterest    Scalar          `json:"bayar_bunga"`
	PaidPenalty     Scalar          `json:"bayar_denda"`
	JournalNo       string          `json:"nojurnal"`
	Branch          string          `json:"cabang"`
}

func (c *Client) FetchCIFDetail(ctx context.Context, cifNo string) (*CIFDetail, error) {
	return fetchDetail[CIFDetail](ctx, c, "fetch CIF detail", "/cif/inquiry/cif/cif", "nocif", cifNo)
}

func (c *Client) FetchSavingDetail(ctx context.Context, accountNo string) (*SavingDetail, error) {
	return fetchDetail[SavingDetail](ctx, c, "fetch saving detail", "/tabungan/inquiry/rekening/tabungan", "id", accountNo)
}

func (c *Client) FetchSavingAccountStatement(ctx context.Context, accountNo string) (*SavingAccountStatement, error) {
	const operation = "fetch saving account statement"
	query := url.Values{"id": {accountNo}}
	var envelope struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	diagnostic, err := c.getJSON(ctx, operation, "/tabungan/inquiry/rekening/historyMutasi?"+query.Encode(), &envelope)
	if err != nil {
		return nil, err
	}
	if envelope.Status != "ok" {
		return nil, applicationFailure(operation, "Fincloud reported a source failure", diagnostic, envelope.Status, "")
	}
	data := bytes.TrimSpace(envelope.Data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) || data[0] != '{' {
		return nil, malformedStatement(operation, "data must be a present object", diagnostic, "statement_data")
	}
	var dataPayload struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &dataPayload); err != nil {
		return nil, malformedStatement(operation, "data was malformed", diagnostic, "statement_data")
	}
	result := bytes.TrimSpace(dataPayload.Result)
	if len(result) == 0 || bytes.Equal(result, []byte("null")) || result[0] != '{' {
		return nil, malformedStatement(operation, "result must be a present object", diagnostic, "statement_result")
	}
	var resultPayload struct {
		Mutations json.RawMessage `json:"mutasi"`
	}
	if err := json.Unmarshal(result, &resultPayload); err != nil {
		return nil, malformedStatement(operation, "result was malformed", diagnostic, "statement_result")
	}
	mutations := bytes.TrimSpace(resultPayload.Mutations)
	if len(mutations) == 0 || bytes.Equal(mutations, []byte("null")) || mutations[0] != '[' {
		return nil, malformedStatement(operation, "mutasi must be a present array", diagnostic, "statement_mutasi")
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(mutations, &rawItems); err != nil || rawItems == nil {
		return nil, malformedStatement(operation, "mutasi must be a present array", diagnostic, "statement_mutasi")
	}
	statement := &SavingAccountStatement{Mutations: make([]SavingAccountStatementItem, len(rawItems))}
	for index, raw := range rawItems {
		item := bytes.TrimSpace(raw)
		if len(item) == 0 || bytes.Equal(item, []byte("null")) || item[0] != '{' {
			return nil, malformedStatement(operation, "mutasi item must be an object", diagnostic, "statement_item")
		}
		if err := json.Unmarshal(item, &statement.Mutations[index]); err != nil {
			return nil, malformedStatement(operation, "mutasi item was malformed", diagnostic, "statement_item")
		}
		if err := statement.Mutations[index].validate(); err != nil {
			return nil, malformedStatement(operation, "mutasi item contained an invalid value", diagnostic, "statement_item")
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, item); err != nil {
			return nil, malformedStatement(operation, "mutasi item was malformed", diagnostic, "statement_item")
		}
		statement.Mutations[index].RawPayload = append(json.RawMessage(nil), compact.Bytes()...)
	}
	return statement, nil
}

func (item SavingAccountStatementItem) validate() error {
	if item.TransactionDate != nil {
		parsed, err := time.Parse("2006-01-02", *item.TransactionDate)
		if err != nil || parsed.Format("2006-01-02") != *item.TransactionDate {
			return fmt.Errorf("invalid transaction date")
		}
	}
	if item.TransactionTime != nil {
		parsed, err := time.Parse("15:04:05", *item.TransactionTime)
		if err != nil || parsed.Format("15:04:05") != *item.TransactionTime {
			return fmt.Errorf("invalid transaction time")
		}
	}
	for _, value := range []*string{item.OpeningBalance, item.Debit, item.Credit, item.ClosingBalance, item.ClosingBalanceEquivalent, item.MidRateDC} {
		if value != nil {
			if _, err := Scalar(*value).Decimal(); err != nil {
				return fmt.Errorf("invalid decimal")
			}
		}
	}
	if item.TransactionRate != nil {
		if _, err := Scalar(item.TransactionRate.String()).Decimal(); err != nil {
			return fmt.Errorf("invalid transaction rate")
		}
	}
	return nil
}

func malformedStatement(operation, message string, diagnostic *DiagnosticPayload, stage string) error {
	if diagnostic == nil {
		diagnostic = &DiagnosticPayload{}
	}
	diagnostic.FailureKind, diagnostic.DecodeStage = "missing_required", stage
	return &Error{Kind: ErrorMalformed, Operation: operation, Message: message, Cause: fmt.Errorf("source contract violation"), diagnostic: diagnostic}
}

func (c *Client) FetchTimeDepositDetail(ctx context.Context, accountNo string) (*TimeDepositDetail, error) {
	return fetchDetail[TimeDepositDetail](ctx, c, "fetch time-deposit detail", "/deposito/inquiry/rekening/deposito", "id", accountNo)
}

func (c *Client) FetchLoanDetail(ctx context.Context, accountNo string) (*LoanDetail, error) {
	return fetchDetail[LoanDetail](ctx, c, "fetch loan detail", "/pinjaman/inquiry/rekening/pinjaman", "id", accountNo)
}

func fetchDetail[T any](ctx context.Context, client *Client, operation, endpoint, parameter, identifier string) (*T, error) {
	query := url.Values{parameter: {identifier}}
	resp, err := client.do(ctx, operation, func(sessionID string) (*http.Request, error) {
		return client.newRequest(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil, sessionID)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, client.upstreamResponseFailure(operation, resp)
	}
	data, body, readErr := client.readResponseBody(resp, true, operation)
	diagnostic := client.responseDiagnostic(resp, body)
	diagnostic.Application = applicationFromBody(body)
	if readErr != nil {
		diagnostic.FailureKind = "body_read"
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "could not read Fincloud detail response", Cause: readErr, diagnostic: diagnostic}
	}
	var envelope struct {
		Status string `json:"status"`
		Data   struct {
			Result json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&envelope); err != nil {
		diagnostic.FailureKind, diagnostic.DecodeStage = decodeFailureKind(err), "detail_envelope"
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "Fincloud detail response was malformed", Cause: err, diagnostic: diagnostic}
	}
	if envelope.Status != "ok" {
		return nil, applicationFailure(operation, "Fincloud reported a source failure", diagnostic, envelope.Status, "")
	}
	if len(bytes.TrimSpace(envelope.Data.Result)) == 0 {
		diagnostic.FailureKind = "missing_result"
		return nil, &Error{Kind: ErrorUpstream, Operation: operation, Message: "Fincloud reported a source failure", diagnostic: diagnostic}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, envelope.Data.Result); err != nil {
		diagnostic.FailureKind, diagnostic.DecodeStage = "malformed_json", "detail_result"
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "Fincloud detail result was malformed", Cause: err, diagnostic: diagnostic}
	}
	var result T
	if err := json.Unmarshal(compact.Bytes(), &result); err != nil {
		diagnostic.FailureKind, diagnostic.DecodeStage = decodeFailureKind(err), "detail_dto"
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "Fincloud detail result could not be decoded", Cause: err, diagnostic: diagnostic}
	}
	attachRawPayload(any(&result), compact.Bytes())
	return &result, nil
}

func attachRawPayload(result any, payload []byte) {
	raw := append(json.RawMessage(nil), payload...)
	switch detail := result.(type) {
	case *CIFDetail:
		detail.RawPayload = raw
	case *SavingDetail:
		detail.RawPayload = raw
	case *TimeDepositDetail:
		detail.RawPayload = raw
	case *LoanDetail:
		detail.RawPayload = raw
	}
}
