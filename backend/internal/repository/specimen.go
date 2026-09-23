package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/blueship581/veterinary-lab-result-review/backend/internal/dto"
	"github.com/blueship581/veterinary-lab-result-review/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SpecimenRepository owns all persistence operations for 检验样本.
type SpecimenRepository interface {
	List(context.Context, dto.PageQuery) (Page[model.Specimen], error)
	Get(context.Context, uint) (model.Specimen, error)
	Create(context.Context, *model.Specimen) error
	Update(context.Context, uint, uint, *model.Specimen) error
	Delete(context.Context, uint) error
	CountByStatus(context.Context) (map[string]int64, error)
	// Split atomically debits the mother amount, creates the children and
	// writes provenance + audit rows. The whole batch rolls back on any error.
	Split(context.Context, *model.Specimen, float64, []model.Specimen, []model.SpecimenSplit, string, string) error
	// HasUndisposedChildren reports whether the specimen has children that are
	// not in the disposed terminal state.
	HasUndisposedChildren(context.Context, uint) (bool, error)
	// Lineage attaches children and provenance to the given specimen.
	Lineage(context.Context, *model.Specimen) error
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
	if err != nil || len(page.Items) == 0 {
		return page, err
	}
	ids := make([]uint, 0, len(page.Items))
	for i := range page.Items {
		ids = append(ids, page.Items[i].ID)
	}
	var children []model.Specimen
	if err := r.db.WithContext(ctx).Where("parent_id IN ?", ids).
		Order("parent_id ASC, split_seq ASC, id ASC").Find(&children).Error; err != nil {
		return Page[model.Specimen]{}, err
	}
	var splits []model.SpecimenSplit
	if err := r.db.WithContext(ctx).Where("parent_id IN ?", ids).
		Order("parent_id ASC, sequence ASC, id ASC").Find(&splits).Error; err != nil {
		return Page[model.Specimen]{}, err
	}
	amountByParentChild := make(map[uint]map[uint]float64, len(ids))
	for _, split := range splits {
		if amountByParentChild[split.ParentID] == nil {
			amountByParentChild[split.ParentID] = make(map[uint]float64)
		}
		amountByParentChild[split.ParentID][split.ChildID] = split.Amount
	}
	childrenByParent := make(map[uint][]model.SpecimenLineage, len(ids))
	for _, child := range children {
		parentID := uint(0)
		if child.ParentID != nil {
			parentID = *child.ParentID
		}
		childrenByParent[parentID] = append(childrenByParent[parentID], model.SpecimenLineage{
			ID: child.ID, Code: child.Code, Name: child.Name, Status: child.Status,
			Version: child.Version, Sequence: child.SplitSeq,
			AvailableAmount: child.AvailableAmount, AvailableUnit: child.AvailableUnit,
			Amount: amountByParentChild[parentID][child.ID], ParentID: parentID,
		})
	}
	for i := range page.Items {
		page.Items[i].Children = childrenByParent[page.Items[i].ID]
	}
	return page, nil
}

func (r *specimenRepository) Get(ctx context.Context, id uint) (model.Specimen, error) {
	var item model.Specimen
	if err := r.db.WithContext(ctx).First(&item, id).Error; err != nil {
		return item, err
	}
	if err := r.Lineage(ctx, &item); err != nil {
		return item, err
	}
	return item, nil
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

func (r *specimenRepository) HasUndisposedChildren(ctx context.Context, id uint) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.Specimen{}).
		Where("parent_id = ? AND status <> ?", id, "disposed").
		Count(&count).Error
	return count > 0, err
}

func (r *specimenRepository) Lineage(ctx context.Context, item *model.Specimen) error {
	var children []model.Specimen
	if err := r.db.WithContext(ctx).
		Where("parent_id = ?", item.ID).
		Order("split_seq ASC, id ASC").
		Find(&children).Error; err != nil {
		return err
	}
	if len(children) == 0 {
		return nil
	}
	var splits []model.SpecimenSplit
	if err := r.db.WithContext(ctx).
		Where("parent_id = ?", item.ID).
		Order("sequence ASC, id ASC").
		Find(&splits).Error; err != nil {
		return err
	}
	amountByChild := make(map[uint]float64, len(splits))
	for _, split := range splits {
		amountByChild[split.ChildID] = split.Amount
	}
	item.Children = make([]model.SpecimenLineage, 0, len(children))
	for _, child := range children {
		parentID := item.ID
		if child.ParentID != nil {
			parentID = *child.ParentID
		}
		item.Children = append(item.Children, model.SpecimenLineage{
			ID: child.ID, Code: child.Code, Name: child.Name, Status: child.Status,
			Version: child.Version, Sequence: child.SplitSeq,
			AvailableAmount: child.AvailableAmount, AvailableUnit: child.AvailableUnit,
			Amount: amountByChild[child.ID], ParentID: parentID,
		})
	}
	return nil
}

func (r *specimenRepository) Split(ctx context.Context, mother *model.Specimen, totalAmount float64, children []model.Specimen, splits []model.SpecimenSplit, actor, requestID string) error {
	if len(children) == 0 || len(children) != len(splits) {
		return errors.New("split batch must contain matching children and provenance rows")
	}
	targetVersion := mother.Version
	expectedVersion := mother.Version - 1
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Re-read the mother row under a row lock (MySQL/Postgres). SQLite has
		// no FOR UPDATE clause; its single-writer database serializes writers
		// and the conditional update below still enforces the optimistic
		// version and amount guard.
		var locked model.Specimen
		query := tx
		if tx.Dialector.Name() != "sqlite" {
			query = tx.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&locked, mother.ID).Error; err != nil {
			return err
		}
		if locked.Version != expectedVersion || locked.AvailableAmount < totalAmount {
			return ErrVersionConflict
		}

		result := tx.Model(&model.Specimen{}).
			Where("id = ? AND version = ? AND available_amount >= ?", mother.ID, expectedVersion, totalAmount).
			Updates(map[string]any{
				"available_amount": gorm.Expr("available_amount - ?", totalAmount),
				"version":          targetVersion,
				"updated_at":       mother.UpdatedAt,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrVersionConflict
		}

		if err := tx.Omit("Children", "BlockedReason").Create(&children).Error; err != nil {
			return err
		}
		for i := range splits {
			splits[i].ChildID = children[i].ID
		}
		if err := tx.Create(&splits).Error; err != nil {
			return err
		}

		if err := appendAudit(tx, actor, requestID, "split", "Specimen", mother.ID,
			mother.Status, mother.Status,
			fmt.Sprintf("split into %d child specimens, debited %.4f", len(children), totalAmount)); err != nil {
			return err
		}
		for _, child := range children {
			if err := appendAudit(tx, actor, requestID, "split-child", "Specimen", child.ID,
				"", child.Status, fmt.Sprintf("created from mother %s", mother.Code)); err != nil {
				return err
			}
		}
		return nil
	})
}
