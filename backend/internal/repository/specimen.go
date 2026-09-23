package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/blueship581/veterinary-lab-result-review/backend/internal/constants"
	"github.com/blueship581/veterinary-lab-result-review/backend/internal/dto"
	"github.com/blueship581/veterinary-lab-result-review/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrDuplicateSplit 表示同一分装批次令牌被重复提交。
	ErrDuplicateSplit = errors.New("split batch was already submitted")
	// ErrInsufficientQuantity 表示母样剩余可用量不足以覆盖本次分装总量。
	ErrInsufficientQuantity = errors.New("available quantity is insufficient for the split")
)

// SplitPlan 是服务层准备、仓储层在单一事务内执行的分装计划。
type SplitPlan struct {
	Parent          model.Specimen
	ExpectedVersion uint
	Token           string
	Parts           []float64
	Children        []model.Specimen
	Reason          string
}

// SpecimenRepository owns all persistence operations for 检验样本.
type SpecimenRepository interface {
	List(context.Context, dto.PageQuery) (Page[model.Specimen], error)
	Get(context.Context, uint) (model.Specimen, error)
	Create(context.Context, *model.Specimen) error
	Update(context.Context, uint, uint, *model.Specimen) error
	Delete(context.Context, uint) error
	CountByStatus(context.Context) (map[string]int64, error)
	Split(context.Context, SplitPlan, string, string) (model.Specimen, []model.Specimen, error)
	SplitExistsByToken(context.Context, string) (bool, error)
	CountPendingChildren(context.Context, uint) (int64, error)
}

type specimenRepository struct {
	store *Store[model.Specimen]
	db    *gorm.DB
}

func NewSpecimenRepository(db *gorm.DB) SpecimenRepository {
	return &specimenRepository{store: NewStore[model.Specimen](db), db: db}
}

func (r *specimenRepository) List(ctx context.Context, q dto.PageQuery) (Page[model.Specimen], error) {
	page, err := r.store.List(ctx, q)
	if err != nil {
		return Page[model.Specimen]{}, err
	}
	if err := r.enrich(ctx, page.Items); err != nil {
		return Page[model.Specimen]{}, err
	}
	return page, nil
}

func (r *specimenRepository) Get(ctx context.Context, id uint) (model.Specimen, error) {
	items := make([]model.Specimen, 1)
	item, err := r.store.Get(ctx, id)
	if err != nil {
		return model.Specimen{}, err
	}
	items[0] = item
	if err := r.enrich(ctx, items); err != nil {
		return model.Specimen{}, err
	}
	return items[0], nil
}

// enrich 一次性补齐父样编码、直接子样摘要与阻断原因，避免 N+1 查询。
func (r *specimenRepository) enrich(ctx context.Context, items []model.Specimen) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(items))
	parentIDs := make(map[uint]struct{})
	for _, item := range items {
		ids = append(ids, item.ID)
		if item.ParentID != 0 {
			parentIDs[item.ParentID] = struct{}{}
		}
	}

	parentCodeByID := make(map[uint]string)
	if len(parentIDs) > 0 {
		parentIDList := make([]uint, 0, len(parentIDs))
		for id := range parentIDs {
			parentIDList = append(parentIDList, id)
		}
		var parents []model.Specimen
		// 父样即使被软删也要保留来源可追溯性。
		if err := r.db.WithContext(ctx).Unscoped().
			Select("id", "code").Where("id IN ?", parentIDList).Find(&parents).Error; err != nil {
			return err
		}
		for _, parent := range parents {
			parentCodeByID[parent.ID] = parent.Code
		}
	}

	var children []model.Specimen
	if err := r.db.WithContext(ctx).
		Where("parent_id IN ?", ids).Order("parent_id ASC, split_sequence ASC, id ASC").
		Find(&children).Error; err != nil {
		return err
	}
	childrenByParent := make(map[uint][]model.SpecimenChild)
	pendingByParent := make(map[uint]int64)
	for _, child := range children {
		childrenByParent[child.ParentID] = append(childrenByParent[child.ParentID], model.SpecimenChild{
			ID: child.ID, Code: child.Code, Name: child.Name, Status: child.Status,
			AvailableQuantity: child.AvailableQuantity, QuantityUnit: child.QuantityUnit,
			SplitSequence: child.SplitSequence, Disposed: child.Status == string(constants.SpecimenStateDisposed),
		})
		if child.Status != string(constants.SpecimenStateDisposed) {
			pendingByParent[child.ParentID]++
		}
	}

	for index := range items {
		item := &items[index]
		if item.ParentID != 0 {
			if code, ok := parentCodeByID[item.ParentID]; ok {
				item.ParentCode = code
			}
		}
		if list, ok := childrenByParent[item.ID]; ok {
			item.Children = list
		}
		item.BlockReasons = buildSpecimenBlockReasons(item, pendingByParent[item.ID])
	}
	return nil
}

// buildSpecimenBlockReasons 汇总样本页需要直接回显的阻断原因（分装与处置两个操作面）。
func buildSpecimenBlockReasons(item *model.Specimen, pendingChildren int64) []string {
	reasons := make([]string, 0, 3)
	if pendingChildren > 0 {
		reasons = append(reasons, fmt.Sprintf("母样尚有 %d 份未处置子样，禁止处置母样", pendingChildren))
	}
	if !constants.SpecimenSplitStates[item.Status] {
		reasons = append(reasons, fmt.Sprintf("当前状态 %s 不允许分装（仅 received/testing 可分装）", item.Status))
	} else if item.AvailableQuantity <= 0 {
		reasons = append(reasons, "剩余可用量为 0，无法分装")
	}
	return reasons
}

func (r *specimenRepository) Create(ctx context.Context, item *model.Specimen) error {
	return r.store.Create(ctx, item)
}
func (r *specimenRepository) Update(ctx context.Context, id, version uint, item *model.Specimen) error {
	return r.store.Update(ctx, id, version, item)
}
func (r *specimenRepository) Delete(ctx context.Context, id uint) error {
	return r.store.Delete(ctx, id)
}
func (r *specimenRepository) CountByStatus(ctx context.Context) (map[string]int64, error) {
	return r.store.CountByStatus(ctx)
}

func (r *specimenRepository) CountPendingChildren(ctx context.Context, parentID uint) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Model(&model.Specimen{}).
		Where("parent_id = ? AND status <> ?", parentID, string(constants.SpecimenStateDisposed)).
		Count(&total).Error
	return total, err
}

// SplitExistsByToken 在业务校验前识别重复提交，确保重复请求始终得到同一结论而不依赖母样余量。
func (r *specimenRepository) SplitExistsByToken(ctx context.Context, token string) (bool, error) {
	var total int64
	err := r.db.WithContext(ctx).Model(&model.SpecimenSplit{}).Where("token = ?", token).Count(&total).Error
	return total > 0, err
}

// Split 在单一数据库事务中完成母样余量扣减、子样生成、批次幂等记录与审计写入。
// 任何一步失败都会回滚，母样余量保持不变。
func (r *specimenRepository) Split(ctx context.Context, plan SplitPlan, actor, requestID string) (model.Specimen, []model.Specimen, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 重复提交：同一批次令牌已成功落库则整批拒绝（母样不动）。
		var duplicateCount int64
		if err := tx.Model(&model.SpecimenSplit{}).Where("token = ?", plan.Token).Count(&duplicateCount).Error; err != nil {
			return err
		}
		if duplicateCount > 0 {
			return ErrDuplicateSplit
		}

		// 2. 母样行锁（MySQL 行锁；SQLite 退化为普通读取，由条件更新兜底）。
		var locked model.Specimen
		if plan.Parent.ID == 0 {
			return gorm.ErrRecordNotFound
		}
		lockQuery := tx.WithContext(ctx)
		if r.db.Dialector.Name() == "mysql" {
			lockQuery = lockQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := lockQuery.First(&locked, plan.Parent.ID).Error; err != nil {
			return err
		}

		total := 0.0
		for _, part := range plan.Parts {
			total += part
		}
		now := time.Now().UTC()

		// 3. 条件扣减：版本一致且扣减后不为负才生效，并发与重复提交在此串行化。
		result := tx.Model(&model.Specimen{}).
			Where("id = ? AND version = ? AND available_quantity >= ? - 1e-9 AND deleted_at IS NULL",
				plan.Parent.ID, plan.ExpectedVersion, total).
			Updates(map[string]any{
				"available_quantity": gorm.Expr("available_quantity - ?", total),
				"version":            plan.ExpectedVersion + 1,
				"updated_at":         now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			if locked.Version != plan.ExpectedVersion {
				return ErrVersionConflict
			}
			return fmt.Errorf("%w: available %v requested %v", ErrInsufficientQuantity, locked.AvailableQuantity, total)
		}

		// 4. 生成连续编号子样。
		if err := tx.Create(&plan.Children).Error; err != nil {
			if isDuplicateKeyError(err) {
				return ErrDuplicateSplit
			}
			return err
		}

		// 5. 落批次记录（token 唯一索引兜底重复提交）。
		batch := model.SpecimenSplit{
			Token: plan.Token, ParentID: locked.ID, ParentCode: locked.Code,
			Count: len(plan.Children), Total: total, Actor: actor, RequestID: requestID, CreatedAt: now,
		}
		if err := tx.Create(&batch).Error; err != nil {
			if isDuplicateKeyError(err) {
				return ErrDuplicateSplit
			}
			return err
		}

		// 6. 同事务审计：母样扣减一条 + 每个子样来源一条。
		if err := appendAudit(tx, actor, requestID, "split", "Specimen", locked.ID,
			locked.Status, locked.Status,
			fmt.Sprintf("split %d aliquots (%s total %v%s), token=%s, reason=%s",
				len(plan.Children), locked.Code, trimQuantity(total), unitSuffix(locked.QuantityUnit), plan.Token, plan.Reason)); err != nil {
			return err
		}
		for _, child := range plan.Children {
			if err := appendAudit(tx, actor, requestID, "split_create", "Specimen", child.ID,
				"", child.Status,
				fmt.Sprintf("aliquot %d from parent %s (token=%s, quantity %v%s)",
					child.SplitSequence, locked.Code, plan.Token, trimQuantity(child.AvailableQuantity), unitSuffix(child.QuantityUnit))); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return model.Specimen{}, nil, err
	}
	parent, err := r.Get(ctx, plan.Parent.ID)
	if err != nil {
		return model.Specimen{}, nil, err
	}
	created := make([]model.Specimen, 0, len(plan.Children))
	if err := r.db.WithContext(ctx).
		Where("split_token = ? AND parent_id = ?", plan.Token, plan.Parent.ID).
		Order("split_sequence ASC, id ASC").Find(&created).Error; err != nil {
		return model.Specimen{}, nil, err
	}
	if err := r.enrich(ctx, created); err != nil {
		return model.Specimen{}, nil, err
	}
	return parent, created, nil
}

func trimQuantity(value float64) float64 {
	return float64(int64(value*10000+0.5)) / 10000
}

func unitSuffix(unit string) string {
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return ""
	}
	return " " + unit
}

// isDuplicateKeyError 同时识别 MySQL 1062 与 SQLite UNIQUE 约束冲突。
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr interface{ Number() uint16 }
	if errors.As(err, &mysqlErr) && mysqlErr.Number() == 1062 {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint failed") || strings.Contains(text, "duplicate entry")
}
