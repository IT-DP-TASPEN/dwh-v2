package ingestionstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"hash"
	"reflect"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/ingestion"
)

const (
	fixedLoadPending   = "pending"
	fixedLoadPublished = "published"
	fixedMemberPending = "pending"
	fixedMemberSuccess = "success"
)

type FixedRepository struct{ db *sqlx.DB }

type FixedSegment struct {
	Index      int
	FileName   string
	AsOfDate   ingestion.CalendarDate
	SourceRows []ingestion.FixedCSVRow
}

func NewFixedRepository(db *sqlx.DB) *FixedRepository { return &FixedRepository{db: db} }

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
	err = retryTransaction(ctx, "begin_fixed_load", func() error {
		var transactionErr error
		loadID, transactionErr = repository.beginLoadTransaction(ctx, ingestionRunID, plan, manifest)
		return transactionErr
	})
	return loadID, err
}

func (repository *FixedRepository) beginLoadTransaction(ctx context.Context, ingestionRunID uint64, plan ingestion.FixedPlan, manifest [32]byte) (uint64, error) {
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin fixed load: %w", err)
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	result, err := tx.ExecContext(ctx, `INSERT INTO fixed_report_loads
		(ingestion_run_id, job_key, period_from, period_to, status, expected_member_count, manifest_checksum)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ingestionRunID, plan.JobKey, plan.Range.From.String(), plan.Range.To.String(), fixedLoadPending, len(plan.Members), manifest[:])
	if err != nil {
		return 0, fmt.Errorf("create fixed load: %w", err)
	}
	loadID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read fixed load id: %w", err)
	}
	memberRows := make([][]any, len(plan.Members))
	for index, member := range plan.Members {
		memberRows[index] = []any{loadID, member.MemberKey, fixedMemberPending}
	}
	if err := insertRows(ctx, tx, "fixed_report_load_members", []string{"load_id", "member_key", "status"}, memberRows); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit fixed load: %w", err)
	}
	committed = true
	return uint64(loadID), nil
}

func (repository *FixedRepository) StageMember(ctx context.Context, definition ingestion.FixedDefinition, loadID uint64, descriptor ingestion.RequestDescriptor, segments []FixedSegment) error {
	if repository == nil || repository.db == nil {
		return fmt.Errorf("fixed repository is not configured")
	}
	specification, err := fixedStorageFor(definition)
	if err != nil {
		return err
	}
	if loadID == 0 || descriptor.MemberKey == "" || len(segments) == 0 {
		return fmt.Errorf("load, member, and at least one source segment are required")
	}
	return retryTransaction(ctx, "stage_fixed_member", func() error {
		return repository.stageMemberTransaction(ctx, specification, definition, loadID, descriptor, segments)
	})
}

func (repository *FixedRepository) stageMemberTransaction(ctx context.Context, specification fixedStorage, definition ingestion.FixedDefinition, loadID uint64, descriptor ingestion.RequestDescriptor, segments []FixedSegment) error {
	memberKey := descriptor.MemberKey
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fixed member staging: %w", err)
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	var memberStatus string
	if err := tx.GetContext(ctx, &memberStatus, `SELECT status FROM fixed_report_load_members WHERE load_id = ? AND member_key = ? FOR UPDATE`, loadID, memberKey); err != nil {
		return fmt.Errorf("lock fixed member: %w", err)
	}
	switch memberStatus {
	case fixedMemberPending:
		// BeginLoad creates pending members without staging rows, and staging plus
		// the success transition commit atomically. Avoid an empty range DELETE:
		// under REPEATABLE READ it gap-locks concurrent members' insert range.
	case fixedMemberSuccess:
		if _, err := tx.ExecContext(ctx, "DELETE FROM `"+specification.stagingTable+"` WHERE load_id = ? AND member_key = ?", loadID, memberKey); err != nil {
			return fmt.Errorf("clear fixed member staging: %w", err)
		}
	default:
		return fmt.Errorf("fixed member status %q cannot stage", memberStatus)
	}
	var loadRange struct {
		JobKey string `db:"job_key"`
		From   string `db:"period_from"`
		To     string `db:"period_to"`
		Status string `db:"status"`
	}
	// Promote is the only parent transition, and the executor joins every
	// StageMember call before invoking it. This consistent read therefore cannot
	// overlap a parent status change for the same load.
	if err := tx.GetContext(ctx, &loadRange, `SELECT job_key, DATE_FORMAT(period_from, '%Y-%m-%d') period_from,
		DATE_FORMAT(period_to, '%Y-%m-%d') period_to, status FROM fixed_report_loads WHERE id = ?`, loadID); err != nil {
		return fmt.Errorf("read fixed load range: %w", err)
	}
	if loadRange.JobKey != definition.Key || loadRange.Status != fixedLoadPending {
		return fmt.Errorf("fixed load is not pending for job %s", definition.Key)
	}
	columns := append([]string{"load_id", "member_key", "row_ordinal", "source_segment_index", "source_row_number", "source_row_checksum", "source_file_name", "period_from", "period_to", "as_of_date"}, specification.columns...)
	if specification.sourceLocation {
		columns = append(columns[:10], append([]string{"source_location_id"}, columns[10:]...)...)
	}
	segments = append([]FixedSegment(nil), segments...)
	sort.Slice(segments, func(left, right int) bool { return segments[left].Index < segments[right].Index })
	rows := make([][]any, 0)
	memberHash := sha256.New()
	var ordinal uint64
	for _, segment := range segments {
		if segment.Index < 0 || segment.AsOfDate.IsZero() {
			return fmt.Errorf("invalid fixed source segment")
		}
		for _, row := range segment.SourceRows {
			if row.SourceRowNumber < 2 || len(row.SourceRowChecksum) != 64 {
				return fmt.Errorf("invalid fixed source row")
			}
			ordinal++
			values := []any{loadID, memberKey, ordinal, segment.Index, row.SourceRowNumber, row.SourceRowChecksum, segment.FileName}
			values = append(values, loadRange.From, loadRange.To, segment.AsOfDate.String())
			if specification.sourceLocation {
				if descriptor.SourceLocationID == "" || row.SourceLocationID != descriptor.SourceLocationID {
					return fmt.Errorf("source location %q does not match descriptor %q", row.SourceLocationID, descriptor.SourceLocationID)
				}
				values = append(values, row.SourceLocationID)
			}
			for _, header := range definition.RequiredHeaders {
				values = append(values, row.Values[header])
			}
			rows = append(rows, values)
			writeChecksumPart(memberHash, row.SourceRowChecksum)
		}
	}
	if err := insertRows(ctx, tx, specification.stagingTable, columns, rows); err != nil {
		return err
	}
	checksum := memberHash.Sum(nil)
	if _, err := tx.ExecContext(ctx, `UPDATE fixed_report_load_members
		SET status = ?, row_count = ?, member_checksum = ?
		WHERE load_id = ? AND member_key = ?`, fixedMemberSuccess, len(rows), checksum, loadID, memberKey); err != nil {
		return fmt.Errorf("complete fixed member: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fixed member: %w", err)
	}
	committed = true
	return nil
}

func (repository *FixedRepository) Promote(ctx context.Context, definition ingestion.FixedDefinition, loadID uint64) error {
	if repository == nil || repository.db == nil {
		return fmt.Errorf("fixed repository is not configured")
	}
	specification, err := fixedStorageFor(definition)
	if err != nil {
		return err
	}
	return retryTransaction(ctx, "promote_fixed_load", func() error {
		return repository.promoteTransaction(ctx, specification, definition, loadID)
	})
}

func (repository *FixedRepository) promoteTransaction(ctx context.Context, specification fixedStorage, definition ingestion.FixedDefinition, loadID uint64) error {
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fixed promotion: %w", err)
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	var load struct {
		JobKey              string `db:"job_key"`
		From                string `db:"period_from"`
		To                  string `db:"period_to"`
		Status              string `db:"status"`
		ExpectedMemberCount int    `db:"expected_member_count"`
		Manifest            []byte `db:"manifest_checksum"`
	}
	if err := tx.GetContext(ctx, &load, `SELECT l.job_key,
		DATE_FORMAT(l.period_from, '%Y-%m-%d') period_from,
		DATE_FORMAT(l.period_to, '%Y-%m-%d') period_to,
		l.status, l.expected_member_count, l.manifest_checksum
		FROM fixed_report_loads l WHERE l.id = ? FOR UPDATE`, loadID); err != nil {
		return fmt.Errorf("lock fixed load: %w", err)
	}
	members := []fixedLoadMember{}
	if err := tx.SelectContext(ctx, &members, `SELECT member_key,status,row_count,member_checksum
		FROM fixed_report_load_members WHERE load_id = ? ORDER BY member_key FOR UPDATE`, loadID); err != nil {
		return fmt.Errorf("lock fixed load members: %w", err)
	}
	if load.JobKey != definition.Key || load.ExpectedMemberCount != len(members) {
		return fmt.Errorf("fixed load is incomplete or belongs to another job")
	}
	for _, member := range members {
		if member.Status != fixedMemberSuccess {
			return fmt.Errorf("fixed load is incomplete or belongs to another job")
		}
	}
	if load.Status != fixedLoadPending && load.Status != fixedLoadPublished {
		return fmt.Errorf("fixed load status %q cannot publish", load.Status)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fixed_report_publications
		(job_key, period_from, period_to, active_load_id, published_at)
		VALUES (?, ?, ?, NULL, NULL)
		ON DUPLICATE KEY UPDATE active_load_id = active_load_id`, load.JobKey, load.From, load.To); err != nil {
		return fmt.Errorf("establish fixed publication scope: %w", err)
	}
	var active sql.NullInt64
	if err := tx.GetContext(ctx, &active, `SELECT active_load_id FROM fixed_report_publications
		WHERE job_key = ? AND period_from = ? AND period_to = ? FOR UPDATE`, load.JobKey, load.From, load.To); err != nil {
		return fmt.Errorf("lock fixed publication: %w", err)
	}
	if active.Valid {
		activeLoadID := uint64(active.Int64)
		if activeLoadID == loadID {
			if err := tx.Commit(); err != nil {
				return err
			}
			committed = true
			return nil
		}
		if activeLoadID > loadID {
			return fmt.Errorf("fixed load %d is stale behind published load %d", loadID, activeLoadID)
		}
	}
	memberKeys := make([]string, len(members))
	for index := range members {
		memberKeys[index] = members[index].Key
	}
	plan := ingestion.FixedPlan{JobKey: load.JobKey, Range: ingestion.FixedDateRangeParams{From: mustDate(load.From), To: mustDate(load.To)}, RequireAllMembers: true}
	for _, memberKey := range memberKeys {
		plan.Members = append(plan.Members, ingestion.RequestDescriptor{MemberKey: memberKey})
	}
	manifest, err := ingestion.FixedManifestChecksum(definition, plan)
	if err != nil || !bytes.Equal(manifest[:], load.Manifest) {
		return fmt.Errorf("fixed load manifest does not match frozen members")
	}
	if err := validateStagedMembers(ctx, tx, specification.stagingTable, loadID, members); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM `"+specification.finalTable+"` WHERE period_from = ? AND period_to = ?", load.From, load.To); err != nil {
		return fmt.Errorf("delete fixed publication scope: %w", err)
	}
	finalColumns := append([]string{"load_id", "row_ordinal", "source_segment_index", "source_row_number", "source_row_checksum", "source_file_name", "period_from", "period_to", "as_of_date"}, specification.columns...)
	if specification.sourceLocation {
		finalColumns = append(finalColumns[:9], append([]string{"source_location_id"}, finalColumns[9:]...)...)
	}
	quoted := make([]string, len(finalColumns))
	for index, column := range finalColumns {
		quoted[index], _ = quoteIdentifier(column)
	}
	query := "INSERT INTO `" + specification.finalTable + "` (" + strings.Join(quoted, ",") + ") SELECT " + strings.Join(quoted, ",") + " FROM `" + specification.stagingTable + "` WHERE load_id = ? ORDER BY member_key, row_ordinal"
	if _, err := tx.ExecContext(ctx, query, loadID); err != nil {
		return fmt.Errorf("promote fixed report: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE fixed_report_publications SET active_load_id = ?, published_at = CURRENT_TIMESTAMP(6)
		WHERE job_key = ? AND period_from = ? AND period_to = ?`, loadID, load.JobKey, load.From, load.To); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE fixed_report_loads SET status = ?, published_at = CURRENT_TIMESTAMP(6) WHERE id = ?`, fixedLoadPublished, loadID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fixed promotion: %w", err)
	}
	committed = true
	return nil
}

type stagedMember struct {
	Count    uint64 `db:"row_count"`
	Checksum []byte `db:"member_checksum"`
}

type fixedLoadMember struct {
	Key      string `db:"member_key"`
	Status   string `db:"status"`
	Count    uint64 `db:"row_count"`
	Checksum []byte `db:"member_checksum"`
}

type stagedAggregate struct {
	count uint64
	hash  hash.Hash
}

func validateStagedMembers(ctx context.Context, tx *sqlx.Tx, stagingTable string, loadID uint64, members []fixedLoadMember) error {
	expected := make(map[string]stagedMember, len(members))
	aggregates := make(map[string]*stagedAggregate, len(members))
	for _, member := range members {
		expected[member.Key] = stagedMember{Count: member.Count, Checksum: member.Checksum}
		aggregates[member.Key] = &stagedAggregate{hash: sha256.New()}
	}
	queryRows, err := tx.QueryxContext(ctx, "SELECT member_key, source_row_checksum FROM `"+stagingTable+"` WHERE load_id = ? ORDER BY member_key, row_ordinal", loadID)
	if err != nil {
		return err
	}
	for queryRows.Next() {
		var key, checksum string
		if err := queryRows.Scan(&key, &checksum); err != nil {
			queryRows.Close()
			return err
		}
		aggregate := aggregates[key]
		if aggregate == nil {
			queryRows.Close()
			return fmt.Errorf("staging contains unknown fixed member %q", key)
		}
		aggregate.count++
		writeChecksumPart(aggregate.hash, checksum)
	}
	if err := queryRows.Close(); err != nil {
		return err
	}
	for key, aggregate := range aggregates {
		member := expected[key]
		if aggregate.count != member.Count || !bytes.Equal(aggregate.hash.Sum(nil), member.Checksum) {
			return fmt.Errorf("fixed member %q staged count/checksum mismatch", key)
		}
	}
	return nil
}

type fixedStorage struct {
	finalTable, stagingTable string
	columns                  []string
	sourceLocation           bool
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
	tables := map[string]string{
		"cif_opening_report": "fincloud_cif_opening_reports", "journal_transaction_report": "fincloud_journal_transaction_reports",
		"balance_sheet_report": "fincloud_balance_sheet_reports", "profit_loss_statement": "fincloud_profit_loss_statements",
		"coa_movement_report": "fincloud_coa_movement_reports", "fund_distribution_report": "fincloud_fund_distribution_reports",
		"vault_mutation_report": "fincloud_vault_mutation_reports", "teller_mutation_report": "fincloud_teller_mutation_reports",
	}
	table := tables[definition.Key]
	if table == "" {
		return fixedStorage{}, fmt.Errorf("unsupported fixed report %q", definition.Key)
	}
	columns := make([]string, len(definition.RequiredHeaders))
	for index, header := range definition.RequiredHeaders {
		columns[index] = ingestion.FixedColumnName(header)
	}
	return fixedStorage{finalTable: table, stagingTable: "stg_" + table, columns: columns, sourceLocation: definition.SourceLocationID}, nil
}

func writeChecksumPart(hash interface{ Write([]byte) (int, error) }, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = hash.Write(length[:])
	_, _ = hash.Write([]byte(value))
}

func mustDate(value string) ingestion.CalendarDate {
	date, _ := ingestion.ParseCalendarDate(value)
	return date
}
