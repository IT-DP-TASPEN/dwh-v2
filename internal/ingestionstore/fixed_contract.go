package ingestionstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/jmoiron/sqlx"
)

const fixedMembersSelect = `SELECT member_key,status,row_count,member_checksum,staged_segment_count,
 COALESCE(expected_segment_count,0) expected_segment_count,
 COALESCE(source_location_id,'') source_location_id,COALESCE(account_code,'') account_code,
 COALESCE(DATE_FORMAT(source_period_from,'%Y-%m-%d'),'') source_period_from,
 COALESCE(DATE_FORMAT(source_period_to,'%Y-%m-%d'),'') source_period_to FROM fixed_report_load_members`

type fixedLoadContract struct {
	IngestionRunID      uint64   `db:"ingestion_run_id"`
	JobKey              string   `db:"job_key"`
	From                string   `db:"period_from"`
	To                  string   `db:"period_to"`
	Status              string   `db:"status"`
	ExpectedMemberCount int      `db:"expected_member_count"`
	Manifest            []byte   `db:"manifest_checksum"`
	Version             uint16   `db:"contract_version"`
	Publication         string   `db:"publication_mode"`
	SourceMode          string   `db:"source_request_mode"`
	MaxDays             int      `db:"source_max_chunk_days"`
	RawVariants         []byte   `db:"source_variants"`
	Variants            []string `db:"-"`
}

func readFixedLoad(ctx context.Context, query sqlx.QueryerContext, id uint64, lock bool) (fixedLoadContract, error) {
	var load fixedLoadContract
	statement := `SELECT ingestion_run_id,job_key,DATE_FORMAT(period_from,'%Y-%m-%d') period_from,DATE_FORMAT(period_to,'%Y-%m-%d') period_to,status,expected_member_count,manifest_checksum,
 COALESCE(contract_version,0) contract_version,COALESCE(publication_mode,'') publication_mode,
 COALESCE(source_request_mode,'') source_request_mode,COALESCE(source_max_chunk_days,0) source_max_chunk_days,source_variants FROM fixed_report_loads WHERE id=?`
	if lock {
		statement += " FOR UPDATE"
	}
	if err := sqlx.GetContext(ctx, query, &load, statement, id); err != nil {
		return load, err
	}
	if len(load.RawVariants) > 0 {
		if err := json.Unmarshal(load.RawVariants, &load.Variants); err != nil {
			return load, fmt.Errorf("invalid frozen source variants")
		}
	}
	return load, nil
}
func (load fixedLoadContract) validate(def ingestion.FixedDefinition) error {
	if load.JobKey != def.Key || load.Version != 2 || load.Publication != string(def.PublicationMode) || load.SourceMode != string(def.SourceRequestMode) || load.MaxDays != def.MaxChunkDays {
		return fmt.Errorf("Fixed load does not implement the current frozen publication contract")
	}
	if load.From < "1000-01-01" || load.To > "9999-12-31" || load.From > load.To {
		return fmt.Errorf("invalid Fixed load calendar range")
	}
	return validateSourceVariants(def, load.Variants)
}
func validateFixedSegment(def ingestion.FixedDefinition, load fixedLoadContract, member fixedLoadMember, segment FixedSegment) error {
	chunks, err := ingestion.FixedSourceChunks(def, mustDate(member.From), mustDate(member.To))
	if err != nil {
		return err
	}
	partitions := max(1, len(load.Variants))
	if member.ExpectedSegments != uint64(len(chunks)*partitions) || segment.Index < 0 || segment.Index >= len(chunks)*partitions {
		return fmt.Errorf("Fixed segment differs from frozen source plan")
	}
	chunk := chunks[segment.Index/partitions]
	variant := ""
	if len(load.Variants) > 0 {
		variant = load.Variants[segment.Index%partitions]
	}
	if segment.SourcePeriodFrom != chunk.From || segment.SourcePeriodTo != chunk.To || segment.AsOfDate != chunk.To || segment.RequestVariant != variant {
		return fmt.Errorf("Fixed segment boundaries/variant differ from frozen source request")
	}
	return nil
}

type fixedStoredSegment struct {
	Key      string `db:"member_key"`
	Index    int    `db:"segment_index"`
	From     string `db:"source_period_from"`
	To       string `db:"source_period_to"`
	AsOf     string `db:"as_of_date"`
	Count    uint64 `db:"row_count"`
	Variant  string `db:"request_variant"`
	Checksum []byte `db:"segment_checksum"`
}
type fixedSegmentIdentity struct {
	member string
	index  int
}

func validateFixedSegments(ctx context.Context, tx *sqlx.Tx, def ingestion.FixedDefinition, loadID uint64, load fixedLoadContract, members []fixedLoadMember) (map[fixedSegmentIdentity]fixedStoredSegment, error) {
	var stored []fixedStoredSegment
	if err := tx.SelectContext(ctx, &stored, `SELECT member_key,segment_index,DATE_FORMAT(source_period_from,'%Y-%m-%d') source_period_from,DATE_FORMAT(source_period_to,'%Y-%m-%d') source_period_to,DATE_FORMAT(as_of_date,'%Y-%m-%d') as_of_date,row_count,COALESCE(request_variant,'') request_variant,segment_checksum FROM fixed_report_load_segments WHERE load_id=? ORDER BY member_key,segment_index FOR UPDATE`, loadID); err != nil {
		return nil, err
	}
	known := map[string]fixedLoadMember{}
	counts := map[string]uint64{}
	rows := map[string]uint64{}
	result := map[fixedSegmentIdentity]fixedStoredSegment{}
	for _, m := range members {
		if m.From < load.From || m.To > load.To || m.ExpectedSegments == 0 {
			return nil, fmt.Errorf("invalid frozen member source coverage")
		}
		known[m.Key] = m
	}
	for _, s := range stored {
		m, ok := known[s.Key]
		if !ok || s.Index != int(counts[s.Key]) {
			return nil, fmt.Errorf("missing or unknown Fixed source segment")
		}
		segment := FixedSegment{Index: s.Index, SourcePeriodFrom: mustDate(s.From), SourcePeriodTo: mustDate(s.To), AsOfDate: mustDate(s.AsOf), RequestVariant: s.Variant}
		if err := validateFixedSegment(def, load, m, segment); err != nil {
			return nil, err
		}
		counts[s.Key]++
		rows[s.Key] += s.Count
		result[fixedSegmentIdentity{s.Key, s.Index}] = s
	}
	for _, m := range members {
		if counts[m.Key] != m.ExpectedSegments || m.StagedSegments != m.ExpectedSegments || rows[m.Key] != m.Count {
			return nil, fmt.Errorf("Fixed member/segment completeness mismatch")
		}
	}
	// Date-series snapshots must include the full frozen location set on every date.
	if def.SnapshotDate {
		locations := map[string]bool{}
		dates := map[string]map[string]bool{}
		for _, m := range members {
			if m.From != m.To || m.Location == "" {
				return nil, fmt.Errorf("invalid snapshot member")
			}
			locations[m.Location] = true
			if dates[m.From] == nil {
				dates[m.From] = map[string]bool{}
			}
			if dates[m.From][m.Location] {
				return nil, fmt.Errorf("duplicate snapshot member")
			}
			dates[m.From][m.Location] = true
		}
		for d := mustDate(load.From); d.String() <= load.To; d = d.AddDays(1) {
			if len(dates[d.String()]) != len(locations) {
				return nil, fmt.Errorf("snapshot date lacks complete locations")
			}
			if d.String() == load.To {
				break
			}
		}
	} else {
		for _, m := range members {
			if m.From != load.From || m.To != load.To {
				return nil, fmt.Errorf("range member differs from parent coverage")
			}
		}
	}
	return result, nil
}
