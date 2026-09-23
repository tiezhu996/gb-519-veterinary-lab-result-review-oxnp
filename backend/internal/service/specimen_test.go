package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blueship581/veterinary-lab-result-review/backend/internal/config"
	"github.com/blueship581/veterinary-lab-result-review/backend/internal/dto"
	"github.com/blueship581/veterinary-lab-result-review/backend/internal/model"
	"github.com/blueship581/veterinary-lab-result-review/backend/internal/repository"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// noopSecurity 仅满足 Transition 的审计依赖，审计由仓储测试单独覆盖。
type noopSecurity struct{}

func (noopSecurity) Login(context.Context, dto.LoginRequest) (dto.LoginResponse, error) {
	return dto.LoginResponse{}, nil
}
func (noopSecurity) Audit(_ context.Context, _, _, _, _ string, _ uint, _, _, _ string) error {
	return nil
}
func (noopSecurity) ListAudits(context.Context, int, int, string) ([]model.AuditLog, int64, error) {
	return nil, 0, nil
}
func (noopSecurity) AuditSummary(context.Context, time.Duration) (model.AuditSummary, error) {
	return model.AuditSummary{}, nil
}
func (noopSecurity) EntityHistory(context.Context, string, uint, int) ([]model.AuditLog, error) {
	return nil, nil
}
func (noopSecurity) RuntimeConfig() config.PublicConfig { return config.PublicConfig{} }

func newSpecimenTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.AuditLog{}, &model.Specimen{}, &model.SpecimenSplit{}); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	return db
}

func seedMother(t *testing.T, db *gorm.DB, code, status string, available float64) model.Specimen {
	t.Helper()
	now := time.Now().UTC()
	mother := model.Specimen{
		BaseModel: model.BaseModel{
			Code: code, Name: "分装母样", Status: status, Version: 1,
			CreatedAt: now, UpdatedAt: now,
		},
		Facility: "Lab", Owner: "operator", Category: "血液", RiskLevel: "medium",
		EffectiveAt: now, Quantity: available, QuantityUnit: "mL", AvailableQuantity: available,
	}
	if err := db.Create(&mother).Error; err != nil {
		t.Fatalf("seed mother: %v", err)
	}
	return mother
}

func splitInput(version uint, parts ...float64) dto.SplitSpecimen {
	return dto.SplitSpecimen{
		ExpectedVersion: version,
		Parts:           parts,
		Reason:          "检验前样本分装",
		ClientToken:     "tok-split-" + randomSuffix(),
	}
}

func randomSuffix() string {
	return time.Now().Format("150405.000000")
}

func TestSpecimenSplitSucceedsAtomicallyWithContinuousCodes(t *testing.T) {
	db := newSpecimenTestDB(t)
	repo := repository.NewSpecimenRepository(db)
	svc := NewSpecimenService(repo, nil)
	ctx := context.Background()
	mother := seedMother(t, db, "SP-100", "received", 100)

	parent, children, err := svc.Split(ctx, mother.ID, splitInput(1, 20, 30, 50), "operator", "split-req-1")
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(children) != 3 {
		t.Fatalf("expected 3 children, got %d", len(children))
	}
	if parent.AvailableQuantity != 0 {
		t.Fatalf("mother available quantity must be fully deducted, got %v", parent.AvailableQuantity)
	}
	if parent.Version != 2 {
		t.Fatalf("mother version must advance to 2, got %d", parent.Version)
	}
	expectedCodes := []string{"SP-100-C01", "SP-100-C02", "SP-100-C03"}
	expectedQty := []float64{20, 30, 50}
	for index, child := range children {
		if child.Code != expectedCodes[index] {
			t.Fatalf("child %d code = %s, want %s", index, child.Code, expectedCodes[index])
		}
		if child.SplitSequence != uint(index+1) {
			t.Fatalf("child %d sequence = %d", index, child.SplitSequence)
		}
		if child.AvailableQuantity != expectedQty[index] {
			t.Fatalf("child %d quantity = %v, want %v", index, child.AvailableQuantity, expectedQty[index])
		}
		if child.ParentID != mother.ID || child.ParentCode != "SP-100" {
			t.Fatalf("child lineage broken: %#v", child)
		}
		if child.SplitToken == "" || child.Status != "received" {
			t.Fatalf("child token/status invalid: %#v", child)
		}
	}
	if len(parent.Children) != 3 || parent.Children[2].Code != "SP-100-C03" {
		t.Fatalf("parent children readback invalid: %#v", parent.Children)
	}

	var batchCount int64
	if err := db.Model(&model.SpecimenSplit{}).Where("parent_id = ?", mother.ID).Count(&batchCount).Error; err != nil {
		t.Fatalf("count splits: %v", err)
	}
	if batchCount != 1 {
		t.Fatalf("expected 1 split batch record, got %d", batchCount)
	}
	var auditCount int64
	if err := db.Model(&model.AuditLog{}).Where("entity_type = ? AND (entity_id = ? OR action = ?)", "Specimen", mother.ID, "split_create").Count(&auditCount).Error; err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if auditCount != 4 { // 1 母样 + 3 子样
		t.Fatalf("expected 4 atomic split audits, got %d", auditCount)
	}
}

func TestSpecimenSplitRejectsInsufficientQuantityWithoutChangingMother(t *testing.T) {
	db := newSpecimenTestDB(t)
	svc := NewSpecimenService(repository.NewSpecimenRepository(db), noopSecurity{})
	ctx := context.Background()
	mother := seedMother(t, db, "SP-200", "received", 40)

	_, _, err := svc.Split(ctx, mother.ID, splitInput(1, 20, 25), "operator", "split-req-2")
	if !errors.Is(err, repository.ErrInsufficientQuantity) {
		t.Fatalf("insufficient quantity must be rejected, got %v", err)
	}
	reread, gErr := svc.Get(ctx, mother.ID)
	if gErr != nil {
		t.Fatalf("reread mother: %v", gErr)
	}
	if reread.AvailableQuantity != 40 || reread.Version != 1 {
		t.Fatalf("mother must stay unchanged after rejection: qty=%v version=%d", reread.AvailableQuantity, reread.Version)
	}
	var childrenCount int64
	if err := db.Model(&model.Specimen{}).Where("parent_id = ?", mother.ID).Count(&childrenCount).Error; err != nil {
		t.Fatalf("count children: %v", err)
	}
	if childrenCount != 0 {
		t.Fatalf("no children may be created on rejected split, got %d", childrenCount)
	}
}

func TestSpecimenSplitRejectsDisallowedStatus(t *testing.T) {
	db := newSpecimenTestDB(t)
	svc := NewSpecimenService(repository.NewSpecimenRepository(db), noopSecurity{})
	ctx := context.Background()
	mother := seedMother(t, db, "SP-300", "hold", 100)

	_, _, err := svc.Split(ctx, mother.ID, splitInput(1, 50, 50), "operator", "split-req-3")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("hold specimen must not be splittable, got %v", err)
	}
}

func TestSpecimenSplitRejectsDuplicateSubmission(t *testing.T) {
	db := newSpecimenTestDB(t)
	svc := NewSpecimenService(repository.NewSpecimenRepository(db), noopSecurity{})
	ctx := context.Background()
	mother := seedMother(t, db, "SP-400", "testing", 100)

	input := splitInput(1, 25, 25)
	if _, _, err := svc.Split(ctx, mother.ID, input, "operator", "split-req-4a"); err != nil {
		t.Fatalf("first split: %v", err)
	}
	// 同一 clientToken 重放（即使携带过期版本号）：整批拒绝，母样余量不再扣减。
	_, _, err := svc.Split(ctx, mother.ID, input, "operator", "split-req-4b")
	if !errors.Is(err, repository.ErrDuplicateSplit) {
		t.Fatalf("duplicate submission must be rejected, got %v", err)
	}
	reread, _ := svc.Get(ctx, mother.ID)
	if reread.AvailableQuantity != 50 || len(reread.Children) != 2 {
		t.Fatalf("duplicate split changed state: qty=%v children=%d", reread.AvailableQuantity, len(reread.Children))
	}
}

func TestSpecimenSplitConcurrentVersionConflictRejectsBatch(t *testing.T) {
	db := newSpecimenTestDB(t)
	svc := NewSpecimenService(repository.NewSpecimenRepository(db), noopSecurity{})
	ctx := context.Background()
	mother := seedMother(t, db, "SP-500", "received", 100)

	// 第一次分装成功，版本从 1 到 2。
	if _, _, err := svc.Split(ctx, mother.ID, splitInput(1, 10, 10), "operator", "split-req-5a"); err != nil {
		t.Fatalf("first split: %v", err)
	}
	// 第二个并发请求仍携带版本 1：条件更新 0 行，必须整批失败。
	_, _, err := svc.Split(ctx, mother.ID, splitInput(1, 10, 10), "operator", "split-req-5b")
	if !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("stale version must conflict, got %v", err)
	}
	reread, _ := svc.Get(ctx, mother.ID)
	if reread.AvailableQuantity != 80 || len(reread.Children) != 2 {
		t.Fatalf("concurrent loser must not change mother: qty=%v children=%d", reread.AvailableQuantity, len(reread.Children))
	}
}

func TestSpecimenTransitionToDisposedBlockedWhileChildrenPending(t *testing.T) {
	db := newSpecimenTestDB(t)
	svc := NewSpecimenService(repository.NewSpecimenRepository(db), noopSecurity{})
	ctx := context.Background()
	mother := seedMother(t, db, "SP-600", "received", 100)

	if _, _, err := svc.Split(ctx, mother.ID, splitInput(1, 40, 60), "operator", "split-req-6"); err != nil {
		t.Fatalf("split: %v", err)
	}
	held, err := svc.Transition(ctx, mother.ID, dto.TransitionRequest{
		Status: "hold", ExpectedVersion: 2, Reason: "进入待处置",
	}, "operator", "to-hold")
	if err != nil {
		t.Fatalf("received -> hold must remain allowed after split: %v", err)
	}
	// 母样 hold -> disposed 在迁移图上合法，但存在未处置子样时必须被业务规则拒绝。
	if _, err := svc.Transition(ctx, mother.ID, dto.TransitionRequest{
		Status: "disposed", ExpectedVersion: held.Version, Reason: "处置母样被阻断",
	}, "operator", "dispose-blocked"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("disposing mother with pending children must fail, got %v", err)
	}
}

func TestSpecimenSplitPartCountMustBeTwoToFive(t *testing.T) {
	db := newSpecimenTestDB(t)
	svc := NewSpecimenService(repository.NewSpecimenRepository(db), noopSecurity{})
	ctx := context.Background()
	mother := seedMother(t, db, "SP-700", "received", 100)

	if _, _, err := svc.Split(ctx, mother.ID, dto.SplitSpecimen{
		ExpectedVersion: 1, Parts: []float64{100}, Reason: "只分一份不允许", ClientToken: "tok-split-single01",
	}, "operator", "split-req-7"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("single part must be invalid, got %v", err)
	}
}
