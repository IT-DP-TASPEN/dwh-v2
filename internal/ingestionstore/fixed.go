package ingestionstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"reflect"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/fixedcoverage"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
)

const (
	fixedLoadPending   = "pending"
	fixedLoadPublished = "published"
	fixedMemberPending = "pending"
	fixedMemberSuccess = "success"
	fixedTerminalSQL   = "?,?,?,?"
)

var fixedTerminalStatuses = []any{
	string(ingestionrun.StatusSucceeded), string(ingestionrun.StatusFailed),
	string(ingestionrun.StatusCancelled), string(ingestionrun.StatusAbandoned),
}

type FixedRepository struct{ db *sqlx.DB }

// FixedCleanupResult reports staging cleanup only; run, load, and member history is retained.
type FixedCleanupResult struct {
	Candidates, Loads int
	Rows              int64
	RowsByTable       map[string]int64
}

type fixedCleanupCandidate struct {
	LoadID       uint64 `db:"load_id"`
	StagingTable string
}

type FixedSegment struct {
	Index            int
	SourcePeriodFrom ingestion.CalendarDate
	SourcePeriodTo   ingestion.CalendarDate
	RequestVariant   string
	FileName         string
	AsOfDate         ingestion.CalendarDate
	SourceRows       []ingestion.FixedCSVRow
}

func NewFixedRepository(db *sqlx.DB) *FixedRepository { return &FixedRepository{db: db} }

var ErrFixedStale = errors.New("Fixed publication candidate is stale")

func (repository *FixedRepository) RequireReady(ctx context.Context) error {
	return fixedcoverage.RequireReady(ctx, repository.db)
}

// CleanupTerminal deletes at most limit discovered loads, one short transaction per load.
func (repository *FixedRepository) CleanupTerminal(ctx context.Context, limit int) (FixedCleanupResult, error) {
	result := FixedCleanupResult{RowsByTable: map[string]int64{}}
	if repository == nil || repository.db == nil || limit < 1 {
		return result, fmt.Errorf("positive Fixed staging cleanup limit is required")
	}
	storages, err := fixedStorages()
	if err != nil {
		return result, err
	}
	candidates, err := repository.fixedCleanupCandidates(ctx, storages, limit)
	if err != nil {
		return result, err
	}
	result.Candidates = len(candidates)
	var firstError error
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			if firstError != nil {
				return result, firstError
			}
			return result, err
		}
		deleted, err := repository.cleanupFixedCandidate(ctx, candidate)
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		if deleted == 0 {
			continue
		}
		result.Loads++
		result.Rows += deleted
		result.RowsByTable[candidate.StagingTable] += deleted
	}
	return result, firstError
}

func (repository *FixedRepository) cleanupFixedCandidate(ctx context.Context, candidate fixedCleanupCandidate) (int64, error) {
	var deleted int64
	err := retryReplaySafeTx(ctx, repository.db, "cleanup_fixed_staging", func(tx *sqlx.Tx) error {
		query := "DELETE candidate FROM `" + candidate.StagingTable + "` candidate " +
			"JOIN fixed_report_loads load_row ON load_row.id=candidate.load_id " +
			"JOIN ingestion_runs run ON run.id=load_row.ingestion_run_id " +
			"WHERE candidate.load_id=? AND run.status IN (" + fixedTerminalSQL + ")"
		args := append([]any{candidate.LoadID}, fixedTerminalStatuses...)
		deleteResult, deleteErr := tx.ExecContext(ctx, query, args...)
		if deleteErr != nil {
			return wrapDatabaseError(deleteErr, "cleanup_fixed_staging", "delete_terminal_load_staging", candidate.StagingTable, 0, 0)
		}
		deleted, deleteErr = deleteResult.RowsAffected()
		return wrapDatabaseError(deleteErr, "cleanup_fixed_staging", "count_deleted_load_staging", candidate.StagingTable, 0, 0)
	})
	return deleted, err
}

func (repository *FixedRepository) fixedCleanupCandidates(ctx context.Context, storages []fixedStorage, limit int) ([]fixedCleanupCandidate, error) {
	candidates := make([]fixedCleanupCandidate, 0, limit)
	for _, storage := range storages {
		remaining := limit - len(candidates)
		if remaining == 0 {
			break
		}
		var loadIDs []uint64
		query := "SELECT DISTINCT candidate.load_id FROM `" + storage.stagingTable + "` candidate " +
			"JOIN fixed_report_loads load_row ON load_row.id=candidate.load_id " +
			"JOIN ingestion_runs run ON run.id=load_row.ingestion_run_id " +
			"WHERE run.status IN (" + fixedTerminalSQL + ") ORDER BY candidate.load_id LIMIT ?"
		args := append(append([]any{}, fixedTerminalStatuses...), remaining)
		if err := repository.db.SelectContext(ctx, &loadIDs, query, args...); err != nil {
			return nil, wrapDatabaseError(err, "cleanup_fixed_staging", "find_terminal_load_staging", storage.stagingTable, 0, 0)
		}
		for _, loadID := range loadIDs {
			candidates = append(candidates, fixedCleanupCandidate{LoadID: loadID, StagingTable: storage.stagingTable})
		}
	}
	return candidates, nil
}

func (repository *FixedRepository) BeginLoad(ctx context.Context, ingestionRunID uint64, definition ingestion.FixedDefinition, plan ingestion.FixedPlan) (uint64, error) {
	if repository == nil || repository.db == nil {
		return 0, fmt.Errorf("fixed repository is not configured")
	}
	if ingestionRunID == 0 {
		return 0, fmt.Errorf("ingestion run is required")
	}
	if _, err := fixedStorageFor(definition); err != nil {
		return 0, err
	}
	manifest, err := ingestion.FixedManifestChecksum(definition, plan)
	if err != nil {
		return 0, err
	}
	var loadID uint64
	err = retryReplaySafeTx(ctx, repository.db, "begin_fixed_load", func(tx *sqlx.Tx) error {
		var transactionErr error
		loadID, transactionErr = repository.beginLoadTransaction(ctx, tx, ingestionRunID, definition, plan, manifest)
		return wrapDatabaseError(transactionErr, "begin_fixed_load", "create_fixed_load", "fixed_report_loads", 0, 0)
	})
	return loadID, wrapDatabaseError(err, "begin_fixed_load", "create_fixed_load", "fixed_report_loads", 0, 0)
}

func (repository *FixedRepository) beginLoadTransaction(ctx context.Context, tx *sqlx.Tx, ingestionRunID uint64, definition ingestion.FixedDefinition, plan ingestion.FixedPlan, manifest [32]byte) (uint64, error) {
	if err := fixedcoverage.RequireReady(ctx, tx); err != nil {
		return 0, err
	}
	if err := validateSourceVariants(definition, plan.SourceVariants); err != nil {
		return 0, err
	}
	variants, _ := json.Marshal(append([]string{}, plan.SourceVariants...))
	result, err := tx.ExecContext(ctx, `INSERT INTO fixed_report_loads
 (ingestion_run_id,job_key,period_from,period_to,status,expected_member_count,manifest_checksum,contract_version,publication_mode,source_request_mode,source_max_chunk_days,source_variants)
 VALUES (?,?,?,?,?,?,?,2,?,?,?,?)`, ingestionRunID, plan.JobKey, plan.Range.From.String(), plan.Range.To.String(), fixedLoadPending, len(plan.Members), manifest[:], definition.PublicationMode, definition.SourceRequestMode, definition.MaxChunkDays, variants)
	if err != nil {
		return 0, fmt.Errorf("create fixed load: %w", err)
	}
	loadID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	var memberRows [][]any
	for _, member := range plan.Members {
		if member.RequestedFrom.String() < plan.Range.From.String() || member.RequestedTo.String() > plan.Range.To.String() {
			return 0, fmt.Errorf("member source range is outside its load")
		}
		chunks, err := ingestion.FixedSourceChunks(definition, member.RequestedFrom, member.RequestedTo)
		if err != nil {
			return 0, err
		}
		partitions := max(1, len(plan.SourceVariants))
		memberRows = append(memberRows, []any{loadID, member.MemberKey, fixedMemberPending, nullableFixedDimension(member.SourceLocationID), nullableFixedDimension(member.AccountCode), member.RequestedFrom.String(), member.RequestedTo.String(), len(chunks) * partitions})
	}
	if err := insertRows(ctx, tx, "fixed_report_load_members", []string{"load_id", "member_key", "status", "source_location_id", "account_code", "source_period_from", "source_period_to", "expected_segment_count"}, memberRows); err != nil {
		return 0, err
	}
	return uint64(loadID), nil
}

func validateSourceVariants(def ingestion.FixedDefinition, variants []string) error {
	if def.Key == "journal_transaction_report" {
		if len(variants) == 0 {
			return fmt.Errorf("frozen Journal transaction types are required")
		}
		for i, v := range variants {
			if v == "" || v == "%" || strings.TrimSpace(v) != v || (i > 0 && variants[i-1] >= v) {
				return fmt.Errorf("canonical exact Journal source variants are required")
			}
		}
	} else if len(variants) != 0 {
		return fmt.Errorf("unexpected Fixed source variants")
	}
	return nil
}

func (repository *FixedRepository) StageMemberSegment(ctx context.Context, definition ingestion.FixedDefinition, loadID uint64, descriptor ingestion.RequestDescriptor, segment FixedSegment) error {
	if repository == nil || repository.db == nil {
		return fmt.Errorf("fixed repository is not configured")
	}
	specification, err := fixedStorageFor(definition)
	if err != nil {
		return err
	}
	if loadID == 0 || descriptor.MemberKey == "" {
		return fmt.Errorf("load and member are required")
	}
	err = retryReplaySafeTx(ctx, repository.db, "stage_fixed_member_segment", func(tx *sqlx.Tx) error {
		return wrapDatabaseError(repository.stageMemberSegmentTransaction(ctx, tx, specification, definition, loadID, descriptor, segment),
			"stage_fixed_member_segment", "insert_staging_rows", specification.stagingTable, 0, 0)
	})
	return wrapDatabaseError(err, "stage_fixed_member_segment", "insert_staging_rows", specification.stagingTable, 0, 0)
}

func (repository *FixedRepository) stageMemberSegmentTransaction(ctx context.Context, tx *sqlx.Tx, specification fixedStorage, definition ingestion.FixedDefinition, loadID uint64, descriptor ingestion.RequestDescriptor, segment FixedSegment) error {
	if err := fixedcoverage.RequireReady(ctx, tx); err != nil {
		return err
	}
	var member fixedLoadMember
	if err := tx.GetContext(ctx, &member, fixedMembersSelect+` WHERE load_id=? AND member_key=? FOR UPDATE`, loadID, descriptor.MemberKey); err != nil {
		return fmt.Errorf("lock fixed member: %w", err)
	}
	if member.Status != fixedMemberPending {
		return fmt.Errorf("fixed member status %q cannot stage", member.Status)
	}
	if segment.Index < 0 || uint64(segment.Index) != member.StagedSegments {
		return fmt.Errorf("invalid or out-of-order fixed source segment %d; want %d", segment.Index, member.StagedSegments)
	}
	load, err := readFixedLoad(ctx, tx, loadID, false)
	if err != nil {
		return err
	}
	if err := load.validate(definition); err != nil {
		return err
	}
	if load.Status != fixedLoadPending {
		return fmt.Errorf("fixed load is not pending")
	}
	if descriptor.RequestedFrom.String() != member.From || descriptor.RequestedTo.String() != member.To || descriptor.SourceLocationID != member.Location || descriptor.AccountCode != member.Account {
		return fmt.Errorf("Fixed descriptor differs from its frozen member")
	}
	if err := validateFixedSegment(definition, load, member, segment); err != nil {
		return err
	}
	if err := ingestion.ValidateFixedCoverage(definition, segment.SourcePeriodFrom, segment.SourcePeriodTo, segment.SourceRows); err != nil {
		return err
	}
	columns := []string{"load_id", "member_key", "row_ordinal", "source_segment_index", "source_row_number", "source_row_checksum", "source_file_name", "period_from", "period_to", "as_of_date"}
	if specification.sourceLocation {
		columns = append(columns, "source_location_id")
	}
	if definition.PublicationMode == ingestion.DateAddressable {
		columns = append(columns, "coverage_date")
	}
	columns = append(columns, specification.columns...)
	var rows [][]any
	segmentHash := sha256.New()
	for index, row := range segment.SourceRows {
		if row.SourceRowNumber < 2 || len(row.SourceRowChecksum) != 64 {
			return fmt.Errorf("invalid fixed source row")
		}
		values := []any{loadID, descriptor.MemberKey, member.Count + uint64(index) + 1, segment.Index, row.SourceRowNumber, row.SourceRowChecksum, segment.FileName, load.From, load.To, segment.AsOfDate.String()}
		if specification.sourceLocation {
			if descriptor.SourceLocationID == "" || row.SourceLocationID != descriptor.SourceLocationID {
				return fmt.Errorf("source location does not match frozen descriptor")
			}
			values = append(values, row.SourceLocationID)
		}
		if definition.PublicationMode == ingestion.DateAddressable {
			if row.CoverageDate.IsZero() {
				return fmt.Errorf("validated coverage date required")
			}
			values = append(values, row.CoverageDate.String())
		}
		for _, header := range definition.RequiredHeaders {
			values = append(values, row.Values[header])
		}
		rows = append(rows, values)
		ingestion.WriteFixedMemberChecksumPart(segmentHash, row.SourceRowChecksum)
	}
	if err := insertRows(ctx, tx, specification.stagingTable, columns, rows); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fixed_report_load_segments
 (load_id,member_key,segment_index,source_period_from,source_period_to,as_of_date,row_count,request_variant,segment_checksum) VALUES (?,?,?,?,?,?,?,?,?)`, loadID, descriptor.MemberKey, segment.Index, segment.SourcePeriodFrom.String(), segment.SourcePeriodTo.String(), segment.AsOfDate.String(), len(rows), nullableFixedDimension(segment.RequestVariant), segmentHash.Sum(nil)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE fixed_report_load_members SET row_count=?,staged_segment_count=? WHERE load_id=? AND member_key=?`, member.Count+uint64(len(rows)), member.StagedSegments+1, loadID, descriptor.MemberKey)
	return err
}

func (repository *FixedRepository) FinalizeMemberCandidate(ctx context.Context, definition ingestion.FixedDefinition, loadID uint64, descriptor ingestion.RequestDescriptor, expectedSegments int, rowCount uint64, checksum [sha256.Size]byte) error {
	if repository == nil || repository.db == nil {
		return fmt.Errorf("fixed repository is not configured")
	}
	if _, err := fixedStorageFor(definition); err != nil {
		return err
	}
	if loadID == 0 || descriptor.MemberKey == "" || expectedSegments < 1 {
		return fmt.Errorf("load, member, and positive expected segment count are required")
	}
	err := retryReplaySafeTx(ctx, repository.db, "finalize_fixed_member_candidate", func(tx *sqlx.Tx) error {
		return repository.finalizeMemberCandidateTransaction(ctx, tx, definition, loadID, descriptor, expectedSegments, rowCount, checksum)
	})
	return wrapDatabaseError(err, "finalize_fixed_member_candidate", "complete_fixed_member", "fixed_report_load_members", 0, 0)
}

func (repository *FixedRepository) finalizeMemberCandidateTransaction(ctx context.Context, tx *sqlx.Tx, definition ingestion.FixedDefinition, loadID uint64, descriptor ingestion.RequestDescriptor, expectedSegments int, rowCount uint64, checksum [sha256.Size]byte) error {
	var member struct {
		Status           string `db:"status"`
		RowCount         uint64 `db:"row_count"`
		SegmentCount     uint64 `db:"staged_segment_count"`
		ExpectedSegments uint64 `db:"expected_segment_count"`
		Checksum         []byte `db:"member_checksum"`
	}
	if err := tx.GetContext(ctx, &member, `SELECT status,row_count,staged_segment_count,member_checksum,COALESCE(expected_segment_count,0) expected_segment_count
		FROM fixed_report_load_members WHERE load_id=? AND member_key=? FOR UPDATE`, loadID, descriptor.MemberKey); err != nil {
		return fmt.Errorf("lock fixed member: %w", err)
	}
	if member.Status == fixedMemberSuccess {
		if member.ExpectedSegments == uint64(expectedSegments) && member.SegmentCount == uint64(expectedSegments) && member.RowCount == rowCount && bytes.Equal(member.Checksum, checksum[:]) {
			return nil
		}
		return fmt.Errorf("completed fixed member metadata does not match")
	}
	if member.Status != fixedMemberPending {
		return fmt.Errorf("fixed member status %q cannot finalize", member.Status)
	}
	var load struct {
		JobKey string `db:"job_key"`
		Status string `db:"status"`
	}
	if err := tx.GetContext(ctx, &load, `SELECT job_key,status FROM fixed_report_loads WHERE id=?`, loadID); err != nil {
		return fmt.Errorf("read fixed load: %w", err)
	}
	if load.JobKey != definition.Key || load.Status != fixedLoadPending {
		return fmt.Errorf("fixed load is not pending for job %s", definition.Key)
	}
	if member.ExpectedSegments != uint64(expectedSegments) || member.SegmentCount != uint64(expectedSegments) || member.RowCount != rowCount {
		return fmt.Errorf("fixed member staged count does not match completed candidate")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE fixed_report_load_members SET status=?,member_checksum=?
		WHERE load_id=? AND member_key=?`, fixedMemberSuccess, checksum[:], loadID, descriptor.MemberKey); err != nil {
		return fmt.Errorf("complete fixed member: %w", err)
	}
	return nil
}

func (repository *FixedRepository) Promote(ctx context.Context, runID uint64, ownerID string, definition ingestion.FixedDefinition, loadID uint64) error {
	return repository.promote(ctx, runID, ownerID, definition, loadID, true)
}

// promoteWithoutRunFence exercises storage publication independently in integration tests.
// Production publication must use Promote so result data and succeeded commit together.
func (repository *FixedRepository) promoteWithoutRunFence(ctx context.Context, definition ingestion.FixedDefinition, loadID uint64) error {
	return repository.promote(ctx, 0, "", definition, loadID, false)
}

func (repository *FixedRepository) promote(ctx context.Context, runID uint64, ownerID string, definition ingestion.FixedDefinition, loadID uint64, fenced bool) error {
	if repository == nil || repository.db == nil {
		return fmt.Errorf("fixed repository is not configured")
	}
	if fenced && (runID == 0 || ownerID == "") {
		return fmt.Errorf("complete fixed publication ownership is required")
	}
	specification, err := fixedStorageFor(definition)
	if err != nil {
		return err
	}
	err = retryReplaySafeTx(ctx, repository.db, "promote_fixed_load", func(tx *sqlx.Tx) error {
		return wrapDatabaseError(repository.promoteTransaction(ctx, tx, runID, ownerID, specification, definition, loadID, fenced),
			"promote_fixed_load", "promote_fixed_load", specification.finalTable, 0, 0)
	})
	if err != nil && fenced && !errors.Is(err, ErrFixedStale) {
		var committed bool
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		checkErr := repository.db.GetContext(checkCtx, &committed, `SELECT EXISTS(
 SELECT 1 FROM ingestion_runs r JOIN fixed_report_loads l ON l.ingestion_run_id=r.id
 WHERE r.id=? AND r.status='succeeded' AND l.id=? AND l.status='published')`, runID, loadID)
		cancel()
		if checkErr == nil && committed {
			return nil
		}
	}
	return wrapDatabaseError(err, "promote_fixed_load", "promote_fixed_load", specification.finalTable, 0, 0)
}

func (repository *FixedRepository) promoteTransaction(ctx context.Context, tx *sqlx.Tx, runID uint64, ownerID string, specification fixedStorage, definition ingestion.FixedDefinition, loadID uint64, fenced bool) error {
	// One durable job row serializes both first publication and existing coverage.
	var lockedJob string
	if err := tx.GetContext(ctx, &lockedJob, `SELECT job_key FROM fixed_report_publication_locks WHERE job_key=? FOR UPDATE`, definition.Key); err != nil {
		return fmt.Errorf("lock Fixed publication job: %w", err)
	}
	if err := fixedcoverage.RequireReady(ctx, tx); err != nil {
		return err
	}
	load, err := readFixedLoad(ctx, tx, loadID, true)
	if err != nil {
		return err
	}
	if err := load.validate(definition); err != nil {
		return err
	}
	members := []fixedLoadMember{}
	if err := tx.SelectContext(ctx, &members, fixedMembersSelect+` WHERE load_id=? ORDER BY member_key FOR UPDATE`, loadID); err != nil {
		return err
	}
	if load.ExpectedMemberCount != len(members) || (fenced && load.IngestionRunID != runID) {
		return fmt.Errorf("fixed load is incomplete or belongs to another run")
	}
	for _, member := range members {
		if member.Status != fixedMemberSuccess {
			return fmt.Errorf("fixed load member is incomplete")
		}
	}
	if load.Status != fixedLoadPending && load.Status != fixedLoadPublished {
		return fmt.Errorf("fixed load cannot publish")
	}
	replay := false
	if definition.PublicationMode == ingestion.DateAddressable {
		type publication struct {
			Date   string `db:"coverage_date"`
			LoadID uint64 `db:"active_load_id"`
		}
		var publications []publication
		if err := tx.SelectContext(ctx, &publications, `SELECT DATE_FORMAT(coverage_date,'%Y-%m-%d') coverage_date,active_load_id FROM fixed_report_date_publications WHERE job_key=? AND coverage_date BETWEEN ? AND ? ORDER BY coverage_date FOR UPDATE`, definition.Key, load.From, load.To); err != nil {
			return err
		}
		equal := 0
		for _, pub := range publications {
			if pub.LoadID > loadID {
				return fmt.Errorf("%w: load %d behind %d on %s", ErrFixedStale, loadID, pub.LoadID, pub.Date)
			}
			if pub.LoadID == loadID {
				equal++
			}
		}
		dateCount := 0
		for d := mustDate(load.From); d.String() <= load.To; d = d.AddDays(1) {
			dateCount++
			if d.String() == load.To {
				break
			}
		}
		replay = load.Status == fixedLoadPublished && equal == dateCount
	} else {
		if _, err := tx.ExecContext(ctx, `INSERT INTO fixed_report_publications (job_key,period_from,period_to,active_load_id,published_at) VALUES (?,?,?,NULL,NULL) ON DUPLICATE KEY UPDATE active_load_id=active_load_id`, definition.Key, load.From, load.To); err != nil {
			return err
		}
		var active sql.NullInt64
		if err := tx.GetContext(ctx, &active, `SELECT active_load_id FROM fixed_report_publications WHERE job_key=? AND period_from=? AND period_to=? FOR UPDATE`, definition.Key, load.From, load.To); err != nil {
			return err
		}
		if active.Valid && uint64(active.Int64) > loadID {
			return fmt.Errorf("%w: load %d behind %d", ErrFixedStale, loadID, active.Int64)
		}
		replay = active.Valid && uint64(active.Int64) == loadID && load.Status == fixedLoadPublished
	}
	if replay {
		if fenced {
			return ingestionrun.FinishSucceededInTx(ctx, tx, runID, ownerID)
		}
		return nil
	}
	plan := ingestion.FixedPlan{JobKey: load.JobKey, Range: ingestion.FixedDateRangeParams{From: mustDate(load.From), To: mustDate(load.To)}, SourceVariants: load.Variants, RequireAllMembers: true}
	for _, m := range members {
		plan.Members = append(plan.Members, ingestion.RequestDescriptor{MemberKey: m.Key, RequestedFrom: mustDate(m.From), RequestedTo: mustDate(m.To), SourceLocationID: m.Location, AccountCode: m.Account})
	}
	manifest, err := ingestion.FixedManifestChecksum(definition, plan)
	if err != nil || !bytes.Equal(manifest[:], load.Manifest) {
		return fmt.Errorf("fixed load manifest differs from frozen requests")
	}
	segments, err := validateFixedSegments(ctx, tx, definition, loadID, load, members)
	if err != nil {
		return err
	}
	if err := validateStagedMembers(ctx, tx, specification.stagingTable, definition, loadID, members, segments); err != nil {
		return err
	}
	if definition.PublicationMode == ingestion.DateAddressable {
		if _, err := tx.ExecContext(ctx, "DELETE target FROM `"+specification.finalTable+"` target FORCE INDEX (idx_fixed_coverage_date) WHERE coverage_date BETWEEN ? AND ?", load.From, load.To); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, "DELETE FROM `"+specification.finalTable+"` WHERE period_from=? AND period_to=?", load.From, load.To); err != nil {
			return err
		}
	}
	columns := []string{"load_id", "row_ordinal", "source_segment_index", "source_row_number", "source_row_checksum", "source_file_name", "period_from", "period_to", "as_of_date"}
	if specification.sourceLocation {
		columns = append(columns, "source_location_id")
	}
	if definition.PublicationMode == ingestion.DateAddressable {
		columns = append(columns, "coverage_date")
	}
	columns = append(columns, specification.columns...)
	var quoted []string
	for _, column := range columns {
		value, _ := quoteIdentifier(column)
		quoted = append(quoted, value)
	}
	list := strings.Join(quoted, ",")
	if _, err := tx.ExecContext(ctx, "INSERT INTO `"+specification.finalTable+"` ("+list+") SELECT "+list+" FROM `"+specification.stagingTable+"` WHERE load_id=? ORDER BY member_key,row_ordinal", loadID); err != nil {
		return err
	}
	if definition.PublicationMode == ingestion.DateAddressable {
		if err := publishFixedDates(ctx, tx, loadID, load); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE fixed_report_publications SET active_load_id=?,published_at=CURRENT_TIMESTAMP(6) WHERE job_key=? AND period_from=? AND period_to=?`, loadID, definition.Key, load.From, load.To); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE fixed_report_loads SET status=?,published_at=CURRENT_TIMESTAMP(6) WHERE id=?`, fixedLoadPublished, loadID); err != nil {
		return err
	}
	if fenced {
		return ingestionrun.FinishSucceededInTx(ctx, tx, runID, ownerID)
	}
	return nil
}

func publishFixedDates(ctx context.Context, tx *sqlx.Tx, loadID uint64, load fixedLoadContract) error {
	var values []string
	var args []any
	flush := func() error {
		if len(values) == 0 {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO fixed_report_date_publications (job_key,coverage_date,active_load_id,published_at) VALUES `+strings.Join(values, ",")+` ON DUPLICATE KEY UPDATE active_load_id=VALUES(active_load_id),published_at=CURRENT_TIMESTAMP(6)`, args...)
		values = nil
		args = nil
		return err
	}
	for date := mustDate(load.From); date.String() <= load.To; date = date.AddDays(1) {
		values = append(values, "(?,?,?,CURRENT_TIMESTAMP(6))")
		args = append(args, load.JobKey, date.String(), loadID)
		if len(values) == 500 {
			if err := flush(); err != nil {
				return err
			}
		}
		if date.String() == load.To {
			break
		}
	}
	return flush()
}

type fixedLoadMember struct {
	Key    string `db:"member_key"`
	Status string `db:"status"`

	Count            uint64 `db:"row_count"`
	Checksum         []byte `db:"member_checksum"`
	StagedSegments   uint64 `db:"staged_segment_count"`
	ExpectedSegments uint64 `db:"expected_segment_count"`
	Location         string `db:"source_location_id"`
	Account          string `db:"account_code"`
	From             string `db:"source_period_from"`
	To               string `db:"source_period_to"`
}

type stagedAggregate struct {
	count uint64
	hash  hash.Hash
}

func validateStagedMembers(ctx context.Context, tx *sqlx.Tx, table string, def ingestion.FixedDefinition, loadID uint64, members []fixedLoadMember, segments map[fixedSegmentIdentity]fixedStoredSegment) error {
	memberAggregates := map[string]*stagedAggregate{}
	segmentAggregates := map[fixedSegmentIdentity]*stagedAggregate{}
	for _, m := range members {
		memberAggregates[m.Key] = &stagedAggregate{hash: sha256.New()}
	}
	for key := range segments {
		segmentAggregates[key] = &stagedAggregate{hash: sha256.New()}
	}
	extra := ""
	if def.PublicationMode == ingestion.DateAddressable {
		raw := "`" + ingestion.FixedColumnName(def.CoverageDateHeader) + "`"
		if def.SnapshotDate {
			raw = "DATE_FORMAT(as_of_date,'%Y-%m-%d')"
		}
		extra = ",DATE_FORMAT(coverage_date,'%Y-%m-%d')," + raw
	}
	queryRows, err := tx.QueryxContext(ctx, "SELECT member_key,source_segment_index,source_row_checksum,DATE_FORMAT(as_of_date,'%Y-%m-%d')"+extra+" FROM `"+table+"` WHERE load_id=? ORDER BY member_key,row_ordinal", loadID)
	if err != nil {
		return err
	}
	defer queryRows.Close()
	for queryRows.Next() {
		var key, checksum, asof string
		var index int
		var coverage, raw sql.NullString
		args := []any{&key, &index, &checksum, &asof}
		if def.PublicationMode == ingestion.DateAddressable {
			args = append(args, &coverage, &raw)
		}
		if err := queryRows.Scan(args...); err != nil {
			return err
		}
		identity := fixedSegmentIdentity{key, index}
		member := memberAggregates[key]
		segment := segmentAggregates[identity]
		source, ok := segments[identity]
		if member == nil || segment == nil || !ok || asof != source.AsOf {
			return fmt.Errorf("staging contains unknown or mismatched source segment")
		}
		if def.PublicationMode == ingestion.DateAddressable {
			date, err := ingestion.ParseFixedCoverageDate(def, raw.String, mustDate(asof))
			if err != nil || !raw.Valid || !coverage.Valid || date.String() != coverage.String || coverage.String < source.From || coverage.String > source.To {
				return fmt.Errorf("staged row has invalid source coverage date")
			}
		}
		member.count++
		segment.count++
		ingestion.WriteFixedMemberChecksumPart(member.hash, checksum)
		ingestion.WriteFixedMemberChecksumPart(segment.hash, checksum)
	}
	if err := queryRows.Err(); err != nil {
		return err
	}
	for _, m := range members {
		a := memberAggregates[m.Key]
		if a.count != m.Count || !bytes.Equal(a.hash.Sum(nil), m.Checksum) {
			return fmt.Errorf("Fixed member staged count/checksum mismatch")
		}
	}
	for key, s := range segments {
		a := segmentAggregates[key]
		if a.count != s.Count || !bytes.Equal(a.hash.Sum(nil), s.Checksum) {
			return fmt.Errorf("Fixed segment staged count/checksum mismatch")
		}
	}
	return nil
}

type fixedStorage struct {
	finalTable, stagingTable string
	columns                  []string
	sourceLocation           bool
}

func fixedStorages() ([]fixedStorage, error) {
	definitions := ingestion.FixedDefinitions()
	storages := make([]fixedStorage, len(definitions))
	for index, definition := range definitions {
		storage, err := fixedStorageFor(definition)
		if err != nil {
			return nil, err
		}
		storages[index] = storage
	}
	return storages, nil
}

func fixedStorageFor(definition ingestion.FixedDefinition) (fixedStorage, error) {
	canonical := false
	for _, candidate := range ingestion.FixedDefinitions() {
		if candidate.Key == definition.Key && reflect.DeepEqual(candidate, definition) {
			canonical = true
			break
		}
	}
	if !canonical {
		return fixedStorage{}, fmt.Errorf("fixed report definition %q is not canonical", definition.Key)
	}
	table, err := ingestion.FixedTableName(definition.Key)
	if err != nil {
		return fixedStorage{}, err
	}
	columns := make([]string, len(definition.RequiredHeaders))
	for index, header := range definition.RequiredHeaders {
		columns[index] = ingestion.FixedColumnName(header)
	}
	return fixedStorage{finalTable: table, stagingTable: "stg_" + table, columns: columns, sourceLocation: definition.SourceLocationID}, nil
}

func mustDate(value string) ingestion.CalendarDate {
	date, _ := ingestion.ParseCalendarDate(value)
	return date
}

func nullableFixedDimension(value string) any {
	if value == "" {
		return nil
	}
	return value
}
