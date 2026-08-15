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
	TransactionDate string `json:"tgl"`
	InstallmentNo   Scalar `json:"angsuranke"`
	PaymentDate     string `json:"tglbayar"`
	Currency        string `json:"currency"`
	DueDate         string `json:"tgljt"`
	TotalPaid       Scalar `json:"totalbayar"`
	PaidPrincipal   Scalar `json:"bayar_pokok"`
	PaidInterest    Scalar `json:"bayar_bunga"`
	PaidPenalty     Scalar `json:"bayar_denda"`
	JournalNo       string `json:"nojurnal"`
	Branch          string `json:"cabang"`
}

func (c *Client) FetchCIFDetail(ctx context.Context, cifNo string) (*CIFDetail, error) {
	return fetchDetail[CIFDetail](ctx, c, "fetch CIF detail", "/cif/inquiry/cif/cif", "nocif", cifNo)
}

func (c *Client) FetchSavingDetail(ctx context.Context, accountNo string) (*SavingDetail, error) {
	return fetchDetail[SavingDetail](ctx, c, "fetch saving detail", "/tabungan/inquiry/rekening/tabungan", "id", accountNo)
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
		return nil, responseError(operation, resp.StatusCode)
	}
	var envelope struct {
		Status string `json:"status"`
		Data   struct {
			Result json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "Fincloud detail response was malformed", Cause: err}
	}
	if envelope.Status != "ok" || len(bytes.TrimSpace(envelope.Data.Result)) == 0 {
		return nil, &Error{Kind: ErrorUpstream, Operation: operation, Message: "Fincloud reported a source failure"}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, envelope.Data.Result); err != nil {
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "Fincloud detail result was malformed", Cause: err}
	}
	var result T
	if err := json.Unmarshal(compact.Bytes(), &result); err != nil {
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "Fincloud detail result could not be decoded", Cause: err}
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
