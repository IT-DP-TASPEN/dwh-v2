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
	class, field, sourcePath, category, reason, safeMessage string
}

func (metadata MapperMetadata) Class() string       { return metadata.class }
func (metadata MapperMetadata) Field() string       { return metadata.field }
func (metadata MapperMetadata) SourcePath() string  { return metadata.sourcePath }
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
		for _, section := range shape.sections {
			for _, candidate := range section.fields {
				if field == candidate.target {
					return true
				}
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
		class: "detail_mapping", field: field, sourcePath: field, category: category, reason: reason, safeMessage: message,
	}, cause: cause}
}

func mapperFieldError(field, sourcePath, category, reason, message string, cause error) error {
	err := mapperError(field, category, reason, message, cause).(*MapperError)
	err.metadata.sourcePath = sourcePath
	return err
}

type DetailRecord struct {
	Domain        DetailDomain
	Identifier    string
	LastFetchedAt time.Time
	Fields        map[string]any
	Sections      map[string]map[string]any
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
	record := DetailRecord{Domain: domain, Identifier: identifier, LastFetchedAt: fetchedAt, Fields: mappedFields, Sections: make(map[string]map[string]any), RawPayload: payload, RawChecksum: sha256Hex(payload), Children: make(map[string][]DetailChildRecord)}
	for _, section := range shape.sections {
		present, err := detailSectionPresent(fields, section.fields)
		if err != nil {
			return DetailRecord{}, fmt.Errorf("%s detail: %w", domain, err)
		}
		if !present {
			continue
		}
		mapped, err := mapFields(fields, section.fields, "")
		if err != nil {
			return DetailRecord{}, fmt.Errorf("%s detail: %w", domain, err)
		}
		record.Sections[section.key] = mapped
	}
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
	target, source, fallback string
	valueType                detailValueType
	required, preserveEmpty  bool
}
type detailChildShape struct {
	sourceKey string
	fields    []detailField
}
type detailSectionShape struct {
	key    string
	fields []detailField
}
type detailShape struct {
	identifierKey string
	fields        []detailField
	sections      []detailSectionShape
	children      []detailChildShape
}

func field(target, source string, valueType detailValueType) detailField {
	return detailField{target: target, source: source, valueType: valueType}
}

func requiredField(target, source string, valueType detailValueType) detailField {
	return detailField{target: target, source: source, valueType: valueType, required: true}
}

func preservedString(target, source string) detailField {
	return detailField{target: target, source: source, valueType: detailString, preserveEmpty: true}
}

func fallbackString(target, primary, fallback string) detailField {
	return detailField{target: target, source: primary, fallback: fallback, valueType: detailString, preserveEmpty: true}
}

func detailShapeFor(domain DetailDomain) (detailShape, error) {
	switch domain {
	case DetailCIF:
		return detailShape{identifierKey: "id", fields: []detailField{
			requiredField("cif_no", "id", detailString), requiredField("customer_name", "namanasabah", detailString),
			field("customer_type", "jenisnasabah", detailString), field("identity_type", "jenisidentitas", detailString),
			fallbackString("ktp_no", "perorangan_noktp", "dataktp_nik"), field("birth_date", "perorangan_tgllahir", detailDate),
			field("cif_open_date", "tglbukacif", detailDate), field("record_created_at", "rec_dibuat_tgljam", detailDateTime),
			preservedString("alt_no", "noalt"), preservedString("document_status", "status_dokumen"),
			preservedString("location_name", "location__locationname"), preservedString("record_created_by", "rec_dibuat_oleh"),
			preservedString("record_created_location", "rec_dibuat_lokasi"), preservedString("record_updated_by", "rec_diupdate_oleh"),
			preservedString("record_updated_location", "rec_diupdate_lokasi"), field("record_updated_at", "rec_diupdate_tgljam", detailDateTime),
			field("record_timestamp", "rec_timestamp", detailDateTime),
		}, sections: []detailSectionShape{
			{key: "personal_profile", fields: []detailField{
				preservedString("birth_place", "perorangan_tempatlahir"), preservedString("gender", "perorangan_jeniskelamin"),
				preservedString("religion", "perorangan_agama"), preservedString("marital_status", "perorangan_statusperkawinan"),
				preservedString("formal_education", "perorangan_pendidikanformal"), preservedString("mother_name", "perorangan_namaibukandung"),
				preservedString("nationality", "perorangan_kewarganegaraan"), preservedString("country_of_origin", "perorangan_negaraasal"),
				preservedString("email", "perorangan_alamatemail"), preservedString("title", "perorangan_gelar"),
				preservedString("member_type", "perorangan_jenisanggota"), field("dependent_count", "perorangan_jumlahtanggungan", detailInteger),
				field("residence_years", "perorangan_lamamenempatitahun", detailInteger), field("residence_months", "perorangan_lamamenempatibulan", detailInteger),
				preservedString("residence_status", "perorangan_statustempattinggal"), preservedString("ethnicity", "perorangan_suku"),
				field("marriage_date", "perorangan_tanggalmenikah", detailDate), preservedString("npwp_no", "perorangan_nonpwp"),
			}},
			{key: "ktp", fields: []detailField{
				preservedString("ktp_name", "dataktp_nama"), field("ktp_birth_date", "dataktp_tgllahir", detailDate),
				preservedString("ktp_religion", "dataktp_agama"), preservedString("ktp_snapshot_address", "dataktp_alamat"),
				preservedString("ktp_valid_for_life", "dataktp_berlakuseumurhidup"), preservedString("ktp_blood_type", "dataktp_golongandarah"),
				preservedString("ktp_gender", "dataktp_jeniskelamin"), preservedString("ktp_nationality", "dataktp_kewarganegaraan"),
				preservedString("ktp_occupation", "dataktp_pekerjaan"), preservedString("ktp_marital_status", "dataktp_statusperkawinan"),
				preservedString("ktp_birth_place", "dataktp_tempatlahir"), preservedString("ktp_issue_place", "dataktp_tempatterbit"),
				field("ktp_issue_date", "dataktp_tglterbit", detailDate), field("ktp_valid_until", "dataktp_tglberlaku", detailDate),
			}},
			{key: "addresses", fields: []detailField{
				preservedString("ktp_address_line_1", "dataalamat_ktp_alamat1"), preservedString("ktp_address_line_2", "dataalamat_ktp_alamat2"),
				preservedString("ktp_subdistrict", "dataalamat_ktp_kelurahan"), preservedString("ktp_district", "dataalamat_ktp_kecamatan"),
				preservedString("ktp_city", "dataalamat_ktp_kota"), preservedString("ktp_province", "dataalamat_ktp_propinsi"),
				preservedString("ktp_postal_code", "dataalamat_ktp_kodepos"), preservedString("ktp_rt", "dataalamat_ktp_rt"), preservedString("ktp_rw", "dataalamat_ktp_rw"),
				preservedString("home_address_line_1", "dataalamat_rumah_alamat1"), preservedString("home_address_line_2", "dataalamat_rumah_alamat2"),
				preservedString("home_subdistrict", "dataalamat_rumah_kelurahan"), preservedString("home_district", "dataalamat_rumah_kecamatan"),
				preservedString("home_city", "dataalamat_rumah_kota"), preservedString("home_province", "dataalamat_rumah_propinsi"),
				preservedString("home_postal_code", "dataalamat_rumah_kodepos"), preservedString("home_rt", "dataalamat_rumah_rt"), preservedString("home_rw", "dataalamat_rumah_rw"),
				preservedString("home_area_code", "dataalamat_rumah_kodearea"), preservedString("home_phone", "dataalamat_rumah_notelp"),
				preservedString("home_mobile", "dataalamat_rumah_nohp"), preservedString("home_fax", "dataalamat_rumah_nofax"),
				preservedString("office_address_line_1", "dataalamat_kantor_alamat1"), preservedString("office_address_line_2", "dataalamat_kantor_alamat2"),
				preservedString("office_subdistrict", "dataalamat_kantor_kelurahan"), preservedString("office_district", "dataalamat_kantor_kecamatan"),
				preservedString("office_city", "dataalamat_kantor_kota"), preservedString("office_province", "dataalamat_kantor_propinsi"),
				preservedString("office_postal_code", "dataalamat_kantor_kodepos"), preservedString("office_rt", "dataalamat_kantor_rt"), preservedString("office_rw", "dataalamat_kantor_rw"),
				preservedString("office_area_code", "dataalamat_kantor_kodearea"), preservedString("office_phone", "dataalamat_kantor_notelp"),
				preservedString("office_mobile", "dataalamat_kantor_nohp"), preservedString("office_fax", "dataalamat_kantor_nofax"),
				preservedString("home_same_as_ktp", "dataalamat_alamatrumahadalahalamatktp"),
				preservedString("office_same_as_deed", "dataalamat_alamatkantoradalahalamatakta"),
				preservedString("mailing_address_type", "dataalamat_alamatsuratmenyurat"),
			}},
			{key: "employment", fields: []detailField{
				preservedString("work_type", "datapekerjaan_jenispekerjaan"), preservedString("job_title", "datapekerjaan_jabatan"),
				preservedString("business_field", "datapekerjaan_bidangusaha"), preservedString("company_name", "datapekerjaan_namaperusahaan"),
				preservedString("previous_company_name", "datapekerjaan_namaperusahaansebelumnya"), preservedString("economic_sector", "datapekerjaan_sektorekonomi"),
				field("work_years", "datapekerjaan_lamabekerjatahun", detailInteger), field("work_months", "datapekerjaan_lamabekerjabulan", detailInteger),
				field("previous_work_years", "datapekerjaan_lamabekerjasebelumnyatahun", detailInteger), field("previous_work_months", "datapekerjaan_lamabekerjasebelumnyabulan", detailInteger),
				field("monthly_net_income", "datapekerjaan_penghasilanbersihperbulan", detailDecimal),
				field("monthly_side_income", "datapekerjaan_penghasilansampinganperbulan", detailDecimal),
				field("monthly_expense", "datapekerjaan_totalpengeluaranperbulan", detailDecimal),
				preservedString("side_business", "datapekerjaan_usahasampingan"),
			}},
			{key: "company", fields: []detailField{
				preservedString("company_npwp_no", "perusahaan_nonpwp"), preservedString("company_business_entity_type", "perusahaan_jenisbadanusaha"),
				preservedString("company_initial_deed_no", "perusahaan_noaktaawalberdiri"), preservedString("company_latest_deed_no", "perusahaan_noaktaakhirberdiri"),
				preservedString("company_initial_deed_place", "perusahaan_tempataktaawal"), preservedString("company_latest_deed_place", "perusahaan_tempataktaakhir"),
				field("company_initial_deed_date", "perusahaan_tglaktaawal", detailDate), field("company_latest_deed_date", "perusahaan_tglaktaakhir", detailDate),
			}},
			{key: "kyc", fields: []detailField{
				preservedString("kyc_source_of_funds", "datakyc_sumberdana"), preservedString("kyc_income_source", "datakyc_sumberpenghasilan"),
				preservedString("kyc_fund_use_purpose", "datakyc_tujuanpenggunaandana"),
				field("kyc_cash_deposit_limit", "datakyc_limittransaksi_setorantunai", detailDecimal),
				field("kyc_noncash_deposit_limit", "datakyc_limittransaksi_setorannontunai", detailDecimal),
				field("kyc_cash_withdrawal_limit", "datakyc_limittransaksi_penarikantunai", detailDecimal),
				field("kyc_noncash_withdrawal_limit", "datakyc_limittransaksi_penarikannontunai", detailDecimal),
				field("kyc_transaction_frequency_limit", "datakyc_limittransaksi_frekuensi", detailInteger),
				field("kyc_company_income", "datakyc_datanasabahperusahaan_penghasilan", detailDecimal),
				preservedString("kyc_company_business_form", "datakyc_datanasabahperusahaan_bentukusaha"),
				preservedString("kyc_company_business_field", "datakyc_datanasabahperusahaan_bidangusaha"),
				preservedString("kyc_company_fund_use", "datakyc_datanasabahperusahaan_tujuanpenggunaan"),
			}},
			{key: "regulatory", fields: []detailField{
				preservedString("sid_alias_name", "datauntuksid_namaalias"), preservedString("sid_debtor_group", "datauntuksid_golongandebitur"),
				preservedString("sid_debtor_city", "datauntuksid_dati2debitur"), preservedString("sid_status", "datauntuksid_status"),
				preservedString("sid_din", "datauntuksid_din"), preservedString("sid_related_party", "datauntuksid_pihakterkait"),
				preservedString("sid_related_party_notes", "datauntuksid_keteranganpihakterkait"), preservedString("sid_exceeds_bmpk", "datauntuksid_melampauibmpk"),
				preservedString("sid_violates_bmpk", "datauntuksid_melanggarbmpk"), preservedString("labul_debtor_group", "datalabul_golongandebitur"),
				preservedString("risk_identity", "profilresiko_identitasnasabah"), preservedString("risk_business_location", "profilresiko_lokasiusaha"),
				preservedString("risk_transaction_count", "profilresiko_jumlahtransaksi"), preservedString("risk_business_activity", "profilresiko_kegiatanusaha"),
				preservedString("risk_ownership_structure", "profilresiko_strukturkepemilikan"), preservedString("risk_product_service_network", "profilresiko_produkjasajaringan"),
				preservedString("risk_other_information", "profilresiko_informasilain"), preservedString("risk_final_summary", "profilresiko_resumeakhir"),
				preservedString("risk_profile", "profilresiko_profil"),
			}},
		}}, nil
	case DetailSaving:
		return detailShape{identifierKey: "norekening", fields: []detailField{
			requiredField("account_no", "norekening", detailString), requiredField("cif_no", "nocif", detailString),
			preservedString("account_name", "namarek"), field("location_id", "locationid", detailString),
			requiredField("beginning_balance", "saldoawal", detailDecimal), requiredField("balance", "saldoakhir", detailDecimal),
			field("blocked_balance", "saldoblokir", detailDecimal), field("debit_mutation", "mutasidebit", detailDecimal),
			field("credit_mutation", "mutasikredit", detailDecimal), field("open_date", "tglbukarekening", detailDate),
			field("closed_date", "tglclosed", detailDate), preservedString("alt_no", "noalt"), preservedString("product_id", "produkid"),
			preservedString("product_savings_type", "produk_jenistabungan"), preservedString("savings_type", "jenistabungan"),
			preservedString("currency", "currency"), preservedString("document_status", "status_dokumen"), field("created_date", "createdate", detailDate),
			field("product_credit_interest_rate", "produk_sukubungakredit", detailDecimal), preservedString("credit_interest_type", "sukubungakredit"),
			preservedString("overdraft", "overdraft"), preservedString("joint_account", "jointaccount"),
			preservedString("opening_purpose", "tujuanpembukaanrekening"), preservedString("source_of_fund", "sumberdana"),
			preservedString("product_bnpl", "produk_bnpl"), preservedString("auto_debit", "produk_transaksiautodebit"),
			preservedString("print_bilyet", "cetakbilyet"), preservedString("print_savings_book", "cetakbukutabungan"),
			preservedString("block_reason", "alasanblokir"), preservedString("block_notes", "keteranganblokir"), preservedString("unblock_reason", "alasanbukablokir"),
			preservedString("block_status", "statusblokir"), field("block_date", "tglblokir", detailDate), field("block_end_date", "tglakhirblokir", detailDate),
			field("unblock_date", "tglbukablokir", detailDate), field("unblock_amount", "nilaibukablokir", detailDecimal),
			field("accrued_balance", "saldoakru", detailDecimal), field("accrued_debit_balance", "saldoakrudebit", detailDecimal),
			field("accrued_credit_interest_balance", "saldoakrubungakredit", detailDecimal), field("active_standing_order_count", "total_so_aktif", detailInteger),
			field("fixed_debit_interest_payment_day", "pembayaranbunga_debit_tgltetap", detailInteger),
			field("fixed_credit_interest_payment_day", "pembayaranbunga_kredit_tgltetap", detailInteger),
			preservedString("marketing_code", "kode_marketing"), preservedString("marketing_notes", "notes_marketing"),
		}}, nil
	case DetailTimeDeposit:
		return detailShape{identifierKey: "id", fields: []detailField{
			requiredField("account_no", "id", detailString), requiredField("cif_no", "nocif", detailString),
			requiredField("nominal", "nominal", detailDecimal), field("accrued_interest", "accrueinterest", detailDecimal),
			field("product_interest_rate", "produk_sukubunga", detailDecimal), field("open_date", "tglbukarekening", detailDate),
			field("maturity_date", "tglpencairan", detailDate), field("location_id", "locationid", detailString),
			preservedString("account_name", "namanasabahdepo"), preservedString("certificate_no", "nobilyet"), preservedString("product_id", "produkid"),
			preservedString("product_deposit_type", "produk_jenisdeposito"), preservedString("currency", "currency"), preservedString("term", "jangkawaktu"),
			preservedString("period", "periode"), preservedString("automatic_rollover", "produk_automaticrollover"),
			preservedString("compound_interest", "produk_bungaberbunga"), preservedString("interest_rate_change", "produk_perubahansukubunga"),
			preservedString("interest_payment_method", "produk_pembayaran_bunga"), preservedString("print_certificate", "produk_cetakbilyet"),
			preservedString("document_status", "status_dokumen"), preservedString("joint_account", "jointaccount"),
			preservedString("joint_account_type", "jointaccounttype"), preservedString("source_of_fund", "sumberdana"),
			preservedString("opening_purpose", "tujuanpembukaanrekening"), field("last_interest_payment_date", "pembayaran_bungaterakhir", detailDate),
			field("next_interest_payment_date", "pembayaran_bungaberikutnya", detailDate), preservedString("source_account_no", "norekeningsumberdana"),
			preservedString("interest_destination_account", "norektujuanbunga"), preservedString("disbursement_account_no", "norekeningpencairan"),
			field("created_date", "createddate", detailDate), preservedString("description", "keterangan"),
		}, children: []detailChildShape{{sourceKey: "mutasideposito", fields: []detailField{
			field("transaction_date", "tgltransaksi", detailDate), field("transaction_type", "jenistransaksi", detailString),
			field("currency", "currency", detailString), field("nominal", "nominal", detailDecimal),
			field("interest_rate", "sukubunga", detailDecimal), field("reference", "referensi", detailString),
			field("branch", "cabang", detailString), field("journal_no", "nojurnal", detailString), preservedString("period", "periode"),
			preservedString("term", "jangkawaktu"), preservedString("officer", "officer"), preservedString("description", "keterangan"),
		}}}}, nil
	case DetailLoan:
		return detailShape{identifierKey: "id", fields: []detailField{
			requiredField("account_no", "id", detailString), requiredField("cif_no", "nocif", detailString),
			field("location_id", "locationid", detailString), field("disbursement_date", "tgl_pencairan", detailDate),
			field("outstanding_principal", "outstandingpinjaman", detailDecimal), field("principal_arrears", "tunggakanpokok", detailDecimal),
			field("interest_arrears", "tunggakanbunga", detailDecimal), field("penalty_arrears", "dendatunggakan", detailDecimal),
			field("dpd", "dpd", detailInteger), field("collectability_bi", "kolekbi", detailInteger),
			field("product_interest_rate", "produk_sukubunga", detailDecimal), field("write_off_date", "tglhapusbuku", detailDate),
			preservedString("application_number", "channeling__nopengajuan"), field("insurance_premium", "channeling__premiajk", detailDecimal),
			preservedString("insurance_company", "channeling__lifeinsurancecompany"), preservedString("collateral_policy_number", "channeling__collateralpolicenumber"),
			preservedString("collateral_type", "channeling__collateraltype"), field("collateral_value", "channeling__collateralvalue", detailDecimal),
			preservedString("alt_no", "noalt"), preservedString("pk_no", "nopk"), preservedString("loan_agreement_no", "noperjanjiankredit"),
			preservedString("product_id", "produkid"), preservedString("product_code", "idproduk"), preservedString("product_loan_type", "produk_jenispinjaman"),
			preservedString("product_installment_type", "produk_jenisangsuran"), preservedString("account_status", "statusrekening"),
			preservedString("document_status", "status_dokumen"), preservedString("currency", "currency"), preservedString("term", "jangkawaktu"),
			preservedString("period", "periode"), field("installment_day", "tgl_angsuran", detailInteger), field("principal_amount", "jmlpokok_pinjaman", detailDecimal),
			field("credit_limit", "plafondlimit", detailDecimal), field("accrued_interest", "accrue", detailDecimal), field("flat_interest_rate", "bungaflat", detailDecimal),
			field("collectability_bpr", "kolekbpr", detailInteger), preservedString("collectability_update", "updatekolekbi"),
			field("arrears_start_date", "tanggalmulaitunggakan", detailDate), field("last_due_date", "tgljtterakhir", detailDate),
			field("next_due_date", "tgljtberikutnya", detailDate), field("last_principal_interest_payment_date", "tglterakhir_bayarpokokdanbunga", detailDate),
			field("next_principal_interest_payment_date", "tglbayarpokokbunga_berikutnya", detailDate), field("close_date", "tgl_tutup", detailDate),
			preservedString("restructure_final_agreement_no", "restruktur_noakad_akhir"), field("restructure_final_agreement_date", "restruktur_tanggalakhirakad", detailDate),
			preservedString("restructure_method", "restruktur_cara"), preservedString("restructure_frequency", "restruktur_frekuensi"),
			preservedString("sid_credit_nature", "sid_sifatkredit"), preservedString("sid_credit_nature_2", "sid_sifatkredit2"),
			preservedString("sid_usage_type", "sid_jenispenggunaan"), preservedString("sid_repayment_source", "sid_sumberdanapelunasan"),
			preservedString("sid_credit_group", "sid_golongankredit"), preservedString("sid_usage_orientation", "sid_orientasipenggunaan"),
			preservedString("sid_economic_sector", "sid_sektorekonomi"), preservedString("sid_economic_sector_2", "sid_sektorekonomi2"),
			preservedString("sid_business_type", "sid_jenisusaha"), preservedString("disbursement_saving_account", "norektab_pencairanpinjaman"),
			preservedString("disbursement_saving_account_2", "norektab_pencairanpinjaman2"), preservedString("installment_payment_account", "norektab_bayarangsuran"),
			preservedString("installment_payment_account_2", "norektab_bayarangsuran2"), preservedString("loan_purpose", "tujuankredit"),
			field("last_month_ppap", "ppapblnterakhir", detailDecimal), field("last_ppap_date", "ppaptglterakhir", detailDate),
			field("write_off_principal_balance", "nilaihapusbuku_saldopinjaman", detailDecimal),
			field("write_off_accrued_interest", "nilaihapusbuku_bungaberjalan", detailDecimal),
			field("write_off_interest_arrears", "nilaihapusbuku_tunggakanbunga", detailDecimal),
			field("write_off_penalty_arrears", "nilaihapusbuku_tunggakandenda", detailDecimal), field("total_write_off", "totalhapusbuku", detailDecimal),
			field("charge_off_principal", "ht_pokok", detailDecimal), field("charge_off_interest", "ht_bunga", detailDecimal),
			preservedString("marketing", "marketing"), preservedString("record_created_by", "rec_dibuat_oleh"),
			preservedString("record_created_location", "rec_dibuat_lokasi"),
		}, children: []detailChildShape{
			{sourceKey: "biayapencairan", fields: []detailField{field("fee_name", "namabiaya", detailString), field("fee_amount", "jumlah_biaya", detailDecimal), field("calculate_dwp", "hitungdwp", detailString)}},
			{sourceKey: "jadwalangsuran", fields: []detailField{
				field("schedule_date", "tanggal", detailDate), field("installment_amount", "angsuran", detailDecimal), field("interest_amount", "bunga", detailDecimal),
				field("principal_amount", "pokok", detailDecimal), field("penalty_amount", "denda", detailDecimal), field("paid_principal", "bayar_pokok", detailDecimal),
				field("paid_interest", "bayar_bunga", detailDecimal), field("paid_penalty", "bayar_denda", detailDecimal), field("remaining_loan", "sisapinjaman", detailDecimal),
				field("installment_no", "angsuranke", detailInteger), field("flat_interest", "bunga_flat", detailDecimal),
				field("flat_principal", "pokok_flat", detailDecimal), field("flat_loan", "pinjaman_flat", detailDecimal), preservedString("payment_status", "statusbayar"),
			}},
			{sourceKey: "historybayar", fields: []detailField{
				field("transaction_date", "tgl", detailDate), field("installment_no", "angsuranke", detailInteger), field("payment_date", "tglbayar", detailDate),
				field("currency", "currency", detailString), field("due_date", "tgljt", detailDate), field("total_paid", "totalbayar", detailDecimal),
				field("paid_principal", "bayar_pokok", detailDecimal), field("paid_interest", "bayar_bunga", detailDecimal), field("paid_penalty", "bayar_denda", detailDecimal),
				field("journal_no", "nojurnal", detailString), field("branch", "cabang", detailString), field("paid_closing_penalty", "bayar_dendapelunasan", detailDecimal),
				field("dwp_nominal", "nominaldwp", detailDecimal), preservedString("description", "keterangan"), preservedString("officer", "officer"), preservedString("authorizer", "otor"),
			}},
		}}, nil
	default:
		return detailShape{}, fmt.Errorf("unsupported detail domain %q", domain)
	}
}

func mapFields(source map[string]any, specifications []detailField, prefix string) (map[string]any, error) {
	mapped := make(map[string]any, len(specifications))
	for _, specification := range specifications {
		value, exists, sourcePath, lookupErr := selectDetailValue(source, specification)
		if lookupErr != nil {
			return nil, mapperFieldError(prefix+specification.target, sourcePath, "structure", "invalid_value", "detail field value is invalid", lookupErr)
		}
		valueString, isString := value.(string)
		if !exists || value == nil || (isString && strings.TrimSpace(valueString) == "" && !specification.preserveEmpty) {
			if specification.required {
				return nil, mapperFieldError(prefix+specification.target, sourcePath, specification.valueType.category(), "required", "required detail field is missing", nil)
			}
			mapped[specification.target] = nil
			continue
		}
		converted, err := convertDetailValue(value, specification.valueType)
		if err != nil {
			return nil, mapperFieldError(prefix+specification.target, sourcePath, specification.valueType.category(), "invalid_value", "detail field value is invalid", err)
		}
		mapped[specification.target] = converted
	}
	return mapped, nil
}

func detailSectionPresent(source map[string]any, specifications []detailField) (bool, error) {
	for _, specification := range specifications {
		value, exists, sourcePath, err := selectDetailValue(source, specification)
		if err != nil {
			return false, mapperFieldError(specification.target, sourcePath, "structure", "invalid_value", "detail field value is invalid", err)
		}
		if exists && value != nil {
			return true, nil
		}
	}
	return false, nil
}

func selectDetailValue(source map[string]any, specification detailField) (any, bool, string, error) {
	value, exists, err := lookupDetailValue(source, specification.source)
	selected := specification.source
	if err != nil {
		return nil, false, selected, err
	}
	if specification.fallback != "" && (!exists || value == nil) {
		value, exists, err = lookupDetailValue(source, specification.fallback)
		selected = specification.fallback
	}
	return value, exists, selected, err
}

func lookupDetailValue(source map[string]any, path string) (any, bool, error) {
	parts := strings.Split(path, "__")
	var current any = source
	for index, part := range parts {
		object, ok := current.(map[string]any)
		if !ok {
			if current == nil || current == false {
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("%s must be an object", strings.Join(parts[:index], "__"))
		}
		value, exists := object[part]
		if !exists {
			return nil, false, nil
		}
		current = value
	}
	return current, true, nil
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
