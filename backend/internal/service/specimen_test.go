package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/blueship581/veterinary-lab-result-review/backend/internal/config"
	"github.com/blueship581/veterinary-lab-result-review/backend/internal/dto"
	"github.com/blueship581/veterinary-lab-result-review/backend/internal/model"
	"github.com/blueship581/veterinary-lab-result-review/backend/internal/repository"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newSpecimenTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.AuditLog{}, &model.Specimen{}, &model.SpecimenSplit{}); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	return db
}

func newSpecimenService(db *gorm.DB) SpecimenService {
	security := NewSecurityService(repository.NewSecurityRepository(db), config.Config{})
	return NewSpecimenService(repository.NewSpecimenRepository(db), security)
}

func createTestMother(t *testing.T, svc SpecimenService, code, status string, amount float64) model.Specimen {
	t.Helper()
	ctx := context.Background()
	mother, err := svc.Create(ctx, dto.CreateSpecimen{
		Code: code, Name: "分装母样", Facility: "Veterinary Lab 1", Owner: "Sample desk",
		Category: "血清", RiskLevel: "medium", MetricValue: 7.2, MetricUnit: "score",
		EffectiveAt: time.Now().UTC(), Evidence: "chain of custody sheet", RelatedCode: "CASE-1",
		AvailableAmount: amount, AvailableUnit: "mL",
	}, "operator", "specimen-create")
	if err != nil {
		t.Fatalf("create mother: %v", err)
	}
	if status != mother.Status {
		updated, err := svc.Transition(ctx, mother.ID, dto.TransitionRequest{
			Status: status, ExpectedVersion: mother.Version, Reason: "move mother into test state",
		}, "operator", "specimen-state-setup")
		if err != nil {
			t.Fatalf("move mother to %s: %v", status, err)
		}
		mother = updated
	}
	return mother
}

func TestSplitDebitsMotherAndCreatesTraceableChildren(t *testing.T) {
	db := newSpecimenTestDB(t)
	svc := newSpecimenService(db)
	ctx := context.Background()
	mother := createTestMother(t, svc, "SPLIT-01", "received", 100)

	result, err := svc.Split(ctx, mother.ID, dto.SplitSpecimen{
		ExpectedVersion: mother.Version, Reason: "PCR and culture aliquots",
		Portions: []dto.SplitSpecimenPortion{{Amount: 20}, {Amount: 30}, {Amount: 10}},
	}, "operator", "split-batch-1")
	if err != nil {
		t.Fatalf("split mother: %v", err)
	}
	if result.Status != "received" || result.Version != mother.Version+1 {
		t.Fatalf("mother state/version changed unexpectedly: status=%s version=%d", result.Status, result.Version)
	}
	if result.AvailableAmount != 40 {
		t.Fatalf("expected 40mL remaining, got %v", result.AvailableAmount)
	}
	if len(result.Children) != 3 {
		t.Fatalf("expected 3 children, got %d", len(result.Children))
	}
	wantCodes := []string{"SPLIT-01-01", "SPLIT-01-02", "SPLIT-01-03"}
	wantAmounts := []float64{20, 30, 10}
	for i, child := range result.Children {
		if child.Code != wantCodes[i] || child.Amount != wantAmounts[i] {
			t.Fatalf("child %d mismatch: %#v", i, child)
		}
		if child.Status != "received" || child.Sequence != uint(i+1) || child.AvailableAmount != wantAmounts[i] {
			t.Fatalf("child %d fields mismatch: %#v", i, child)
		}
		if child.ParentID != mother.ID {
			t.Fatalf("child %d not linked to mother", i)
		}
	}

	var splits []model.SpecimenSplit
	if err := db.Where("parent_id = ?", mother.ID).Order("sequence").Find(&splits).Error; err != nil {
		t.Fatalf("load provenance: %v", err)
	}
	if len(splits) != 3 {
		t.Fatalf("expected 3 provenance rows, got %d", len(splits))
	}
	for i, split := range splits {
		if split.Sequence != uint(i+1) || split.ChildCode != wantCodes[i] || split.RequestID != "split-batch-1" ||
			split.Actor != "operator" || split.ParentRemaining != 40 {
			t.Fatalf("provenance row %d mismatch: %#v", i, split)
		}
	}

	var auditCount int64
	if err := db.Model(&model.AuditLog{}).
		Where("entity_type = 'Specimen' AND request_id = ?", "split-batch-1").Count(&auditCount).Error; err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if auditCount != 4 { // one mother row plus one row per child
		t.Fatalf("expected 4 split audits, got %d", auditCount)
	}

	// Reload after refresh: lineage must remain readable.
	reread, err := svc.Get(ctx, mother.ID)
	if err != nil {
		t.Fatalf("reread mother: %v", err)
	}
	if len(reread.Children) != 3 || reread.AvailableAmount != 40 {
		t.Fatalf("lineage lost after refresh: %#v", reread.Children)
	}
}

func TestSplitContinuesCodesAcrossBatchesAndKeepsTestingState(t *testing.T) {
	db := newSpecimenTestDB(t)
	svc := newSpecimenService(db)
	ctx := context.Background()
	mother := createTestMother(t, svc, "SPLIT-02", "testing", 100)

	first, err := svc.Split(ctx, mother.ID, dto.SplitSpecimen{
		ExpectedVersion: mother.Version, Reason: "first aliquot batch",
		Portions: []dto.SplitSpecimenPortion{{Amount: 10}, {Amount: 20}, {Amount: 30}},
	}, "operator", "split-batch-a")
	if err != nil {
		t.Fatalf("first split: %v", err)
	}
	if first.Status != "testing" {
		t.Fatalf("testing mother must keep testing state, got %s", first.Status)
	}

	second, err := svc.Split(ctx, mother.ID, dto.SplitSpecimen{
		ExpectedVersion: first.Version, Reason: "second aliquot batch",
		Portions: []dto.SplitSpecimenPortion{{Amount: 5}, {Amount: 5}},
	}, "operator", "split-batch-b")
	if err != nil {
		t.Fatalf("second split: %v", err)
	}
	if second.AvailableAmount != 30 || second.Version != first.Version+1 || len(second.Children) != 5 {
		t.Fatalf("unexpected mother after second batch: amount=%v version=%d children=%d",
			second.AvailableAmount, second.Version, len(second.Children))
	}
	if second.Children[3].Code != "SPLIT-02-04" || second.Children[4].Code != "SPLIT-02-05" {
		t.Fatalf("codes are not continuous: %s %s", second.Children[3].Code, second.Children[4].Code)
	}
}

func TestSplitRejectsInsufficientAmountWholeBatch(t *testing.T) {
	db := newSpecimenTestDB(t)
	svc := newSpecimenService(db)
	ctx := context.Background()
	mother := createTestMother(t, svc, "SPLIT-03", "received", 25)

	_, err := svc.Split(ctx, mother.ID, dto.SplitSpecimen{
		ExpectedVersion: mother.Version, Reason: "oversized aliquot request",
		Portions: []dto.SplitSpecimenPortion{{Amount: 20}, {Amount: 10}},
	}, "operator", "split-overflow")
	if !errors.Is(err, ErrInsufficientAmount) {
		t.Fatalf("expected ErrInsufficientAmount, got %v", err)
	}

	reread, err := svc.Get(ctx, mother.ID)
	if err != nil {
		t.Fatalf("reread mother: %v", err)
	}
	if reread.AvailableAmount != 25 || reread.Version != mother.Version || len(reread.Children) != 0 {
		t.Fatalf("mother changed on rejected batch: amount=%v version=%d children=%d",
			reread.AvailableAmount, reread.Version, len(reread.Children))
	}
	var specimenCount, splitCount int64
	db.Model(&model.Specimen{}).Where("parent_id = ?", mother.ID).Count(&specimenCount)
	db.Model(&model.SpecimenSplit{}).Where("parent_id = ?", mother.ID).Count(&splitCount)
	if specimenCount != 0 || splitCount != 0 {
		t.Fatalf("rejected batch persisted rows: children=%d provenance=%d", specimenCount, splitCount)
	}
}

func TestSplitRejectsDisallowedStatusAndBadPortionCounts(t *testing.T) {
	db := newSpecimenTestDB(t)
	svc := newSpecimenService(db)
	ctx := context.Background()
	mother := createTestMother(t, svc, "SPLIT-04", "hold", 100)

	if _, err := svc.Split(ctx, mother.ID, dto.SplitSpecimen{
		ExpectedVersion: mother.Version, Reason: "hold cannot be split",
		Portions: []dto.SplitSpecimenPortion{{Amount: 10}, {Amount: 10}},
	}, "operator", "split-status-denied"); !errors.Is(err, ErrSplitNotAllowed) {
		t.Fatalf("expected ErrSplitNotAllowed, got %v", err)
	}

	backToReceived, err := svc.Transition(ctx, mother.ID, dto.TransitionRequest{
		Status: "testing", ExpectedVersion: mother.Version, Reason: "return to splittable state",
	}, "operator", "split-setup")
	if err != nil {
		t.Fatalf("move mother: %v", err)
	}
	if _, err := svc.Split(ctx, mother.ID, dto.SplitSpecimen{
		ExpectedVersion: backToReceived.Version, Reason: "only one portion",
		Portions: []dto.SplitSpecimenPortion{{Amount: 10}},
	}, "operator", "split-too-few"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("single portion must be rejected, got %v", err)
	}
	six := make([]dto.SplitSpecimenPortion, 6)
	for i := range six {
		six[i] = dto.SplitSpecimenPortion{Amount: 1}
	}
	if _, err := svc.Split(ctx, mother.ID, dto.SplitSpecimen{
		ExpectedVersion: backToReceived.Version, Reason: "six portions", Portions: six,
	}, "operator", "split-too-many"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("six portions must be rejected, got %v", err)
	}
	if _, err := svc.Split(ctx, mother.ID, dto.SplitSpecimen{
		ExpectedVersion: backToReceived.Version, Reason: "zero amount portion",
		Portions: []dto.SplitSpecimenPortion{{Amount: 0}, {Amount: 10}},
	}, "operator", "split-zero"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("non-positive portion must be rejected, got %v", err)
	}
}

func TestDuplicateSplitSubmissionAndStaleVersionRejected(t *testing.T) {
	db := newSpecimenTestDB(t)
	svc := newSpecimenService(db)
	ctx := context.Background()
	mother := createTestMother(t, svc, "SPLIT-05", "received", 100)
	request := dto.SplitSpecimen{
		ExpectedVersion: mother.Version, Reason: "duplicate click batch",
		Portions: []dto.SplitSpecimenPortion{{Amount: 10}, {Amount: 10}},
	}
	if _, err := svc.Split(ctx, mother.ID, request, "operator", "split-dup-1"); err != nil {
		t.Fatalf("first split: %v", err)
	}
	// Replaying the identical request (same expectedVersion) must be rejected
	// and must not create a second batch.
	if _, err := svc.Split(ctx, mother.ID, request, "operator", "split-dup-2"); !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("duplicate submission must conflict, got %v", err)
	}
	reread, err := svc.Get(ctx, mother.ID)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	if reread.Version != 2 || reread.AvailableAmount != 80 || len(reread.Children) != 2 {
		t.Fatalf("duplicate batch mutated mother: %#v", reread)
	}
	var splitRows int64
	db.Model(&model.SpecimenSplit{}).Where("parent_id = ?", mother.ID).Count(&splitRows)
	if splitRows != 2 {
		t.Fatalf("expected only the first batch provenance (2 rows), got %d", splitRows)
	}
}

func TestMotherCannotBeDisposedWhileChildrenUndisposed(t *testing.T) {
	db := newSpecimenTestDB(t)
	svc := newSpecimenService(db)
	ctx := context.Background()
	mother := createTestMother(t, svc, "SPLIT-06", "received", 100)
	split, err := svc.Split(ctx, mother.ID, dto.SplitSpecimen{
		ExpectedVersion: mother.Version, Reason: "dispose guard batch",
		Portions: []dto.SplitSpecimenPortion{{Amount: 10}, {Amount: 10}},
	}, "operator", "split-guard")
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	// received -> hold is allowed even with open children; only disposal blocks.
	onHold, err := svc.Transition(ctx, mother.ID, dto.TransitionRequest{
		Status: "hold", ExpectedVersion: split.Version, Reason: "quarantine mother",
	}, "operator", "mother-hold")
	if err != nil {
		t.Fatalf("hold mother: %v", err)
	}
	if _, err := svc.Transition(ctx, mother.ID, dto.TransitionRequest{
		Status: "disposed", ExpectedVersion: onHold.Version, Reason: "try disposal early",
	}, "operator", "mother-dispose-denied"); !errors.Is(err, ErrUndisposedChildren) {
		t.Fatalf("disposal with open children must be rejected, got %v", err)
	}

	// Dispose both children (received -> hold -> disposed), then the mother.
	for _, child := range split.Children {
		held, err := svc.Transition(ctx, child.ID, dto.TransitionRequest{
			Status: "hold", ExpectedVersion: child.Version, Reason: "child quarantine",
		}, "operator", "child-hold")
		if err != nil {
			t.Fatalf("hold child %s: %v", child.Code, err)
		}
		if _, err := svc.Transition(ctx, child.ID, dto.TransitionRequest{
			Status: "disposed", ExpectedVersion: held.Version, Reason: "child disposal",
		}, "operator", "child-dispose"); err != nil {
			t.Fatalf("dispose child %s: %v", child.Code, err)
		}
	}
	disposed, err := svc.Transition(ctx, mother.ID, dto.TransitionRequest{
		Status: "disposed", ExpectedVersion: onHold.Version, Reason: "children already disposed",
	}, "operator", "mother-dispose-ok")
	if err != nil {
		t.Fatalf("dispose mother after children: %v", err)
	}
	if disposed.Status != "disposed" {
		t.Fatalf("mother not disposed: %s", disposed.Status)
	}
}

func TestDecorateBlockedReasonExposedOnList(t *testing.T) {
	db := newSpecimenTestDB(t)
	svc := newSpecimenService(db)
	ctx := context.Background()
	mother := createTestMother(t, svc, "SPLIT-07", "received", 100)
	if _, err := svc.Split(ctx, mother.ID, dto.SplitSpecimen{
		ExpectedVersion: mother.Version, Reason: "blocked reason batch",
		Portions: []dto.SplitSpecimenPortion{{Amount: 10}, {Amount: 10}},
	}, "operator", "split-reason"); err != nil {
		t.Fatalf("split: %v", err)
	}
	page, err := svc.List(ctx, dto.PageQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var blocked string
	for _, item := range page.Items {
		if item.ID == mother.ID {
			blocked = item.BlockedReason
		}
	}
	if blocked == "" {
		t.Fatal("mother with undisposed children must expose a blocked reason")
	}
}

func TestConcurrentSplitOnlyOneBatchCommits(t *testing.T) {
	db := newSpecimenTestDB(t)
	svc := newSpecimenService(db)
	ctx := context.Background()
	mother := createTestMother(t, svc, "SPLIT-08", "received", 100)

	const contenders = 8
	var wg sync.WaitGroup
	results := make(chan error, contenders)
	start := make(chan struct{})
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Split(ctx, mother.ID, dto.SplitSpecimen{
				ExpectedVersion: mother.Version, Reason: "concurrent batch race",
				Portions: []dto.SplitSpecimenPortion{{Amount: 10}, {Amount: 10}},
			}, "operator", "split-race")
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var succeeded, conflicts int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, repository.ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if succeeded != 1 || conflicts != contenders-1 {
		t.Fatalf("expected exactly 1 success and %d conflicts, got %d and %d", contenders-1, succeeded, conflicts)
	}

	final, err := svc.Get(ctx, mother.ID)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	if final.Version != mother.Version+1 || final.AvailableAmount != 80 || len(final.Children) != 2 {
		t.Fatalf("concurrent splits corrupted mother: version=%d amount=%v children=%d",
			final.Version, final.AvailableAmount, len(final.Children))
	}
	var provenance int64
	db.Model(&model.SpecimenSplit{}).Where("parent_id = ?", mother.ID).Count(&provenance)
	if provenance != 2 {
		t.Fatalf("expected provenance from one batch only (2 rows), got %d", provenance)
	}
}
