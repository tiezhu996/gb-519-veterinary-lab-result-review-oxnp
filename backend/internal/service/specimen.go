package service

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/blueship581/veterinary-lab-result-review/backend/internal/constants"
	"github.com/blueship581/veterinary-lab-result-review/backend/internal/dto"
	"github.com/blueship581/veterinary-lab-result-review/backend/internal/model"
	"github.com/blueship581/veterinary-lab-result-review/backend/internal/repository"
)

type SpecimenService interface {
	List(context.Context, dto.PageQuery) (repository.Page[model.Specimen], error)
	Get(context.Context, uint) (model.Specimen, error)
	Create(context.Context, dto.CreateSpecimen, string, string) (model.Specimen, error)
	Update(context.Context, uint, dto.UpdateSpecimen, string, string) (model.Specimen, error)
	Transition(context.Context, uint, dto.TransitionRequest, string, string) (model.Specimen, error)
	Split(context.Context, uint, dto.SplitSpecimen, string, string) (model.Specimen, error)
	Delete(context.Context, uint, string, string) error
	StatusCounts(context.Context) (map[string]int64, error)
}

type specimenService struct {
	repository repository.SpecimenRepository
	security   SecurityService
}

func NewSpecimenService(repo repository.SpecimenRepository, security SecurityService) SpecimenService {
	return &specimenService{repository: repo, security: security}
}

func (s *specimenService) List(ctx context.Context, query dto.PageQuery) (repository.Page[model.Specimen], error) {
	page, err := s.repository.List(ctx, query)
	if err != nil {
		return page, err
	}
	for i := range page.Items {
		s.decorateBlockedReason(ctx, &page.Items[i])
	}
	return page, nil
}

func (s *specimenService) Get(ctx context.Context, id uint) (model.Specimen, error) {
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return item, err
	}
	s.decorateBlockedReason(ctx, &item)
	return item, nil
}

func (s *specimenService) Create(ctx context.Context, input dto.CreateSpecimen, actor, requestID string) (model.Specimen, error) {
	if err := validateSpecimenBusinessFields(input.Code, input.Name, input.Facility, input.Owner); err != nil {
		return model.Specimen{}, err
	}
	item := model.Specimen{
		BaseModel: model.BaseModel{
			Code: strings.ToUpper(strings.TrimSpace(input.Code)), Name: strings.TrimSpace(input.Name),
			Status: model.SpecimenInitialStatus, Version: 1, Description: strings.TrimSpace(input.Description),
		},
		Facility: strings.TrimSpace(input.Facility), Owner: strings.TrimSpace(input.Owner),
		Category: strings.TrimSpace(input.Category), RiskLevel: input.RiskLevel,
		MetricValue: input.MetricValue, MetricUnit: strings.TrimSpace(input.MetricUnit),
		EffectiveAt: input.EffectiveAt.UTC(), Evidence: strings.TrimSpace(input.Evidence),
		RelatedCode:     strings.ToUpper(strings.TrimSpace(input.RelatedCode)),
		AvailableAmount: input.AvailableAmount, AvailableUnit: strings.TrimSpace(input.AvailableUnit),
	}
	if err := s.repository.Create(ctx, &item); err != nil {
		return model.Specimen{}, fmt.Errorf("create 检验样本: %w", err)
	}
	_ = s.security.Audit(ctx, actor, requestID, "create", "Specimen", item.ID, "", item.Status, "created 检验样本")
	return s.Get(ctx, item.ID)
}

func (s *specimenService) Update(ctx context.Context, id uint, input dto.UpdateSpecimen, actor, requestID string) (model.Specimen, error) {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.Specimen{}, err
	}
	if err := validateSpecimenBusinessFields(current.Code, input.Name, input.Facility, input.Owner); err != nil {
		return model.Specimen{}, err
	}
	current.Name = strings.TrimSpace(input.Name)
	current.Description = strings.TrimSpace(input.Description)
	current.Facility = strings.TrimSpace(input.Facility)
	current.Owner = strings.TrimSpace(input.Owner)
	current.Category = strings.TrimSpace(input.Category)
	current.RiskLevel = input.RiskLevel
	current.MetricValue = input.MetricValue
	current.MetricUnit = strings.TrimSpace(input.MetricUnit)
	current.EffectiveAt = input.EffectiveAt.UTC()
	current.Evidence = strings.TrimSpace(input.Evidence)
	current.RelatedCode = strings.ToUpper(strings.TrimSpace(input.RelatedCode))
	current.AvailableAmount = input.AvailableAmount
	current.AvailableUnit = strings.TrimSpace(input.AvailableUnit)
	current.Version = input.ExpectedVersion + 1
	current.UpdatedAt = time.Now().UTC()
	if err := s.repository.Update(ctx, id, input.ExpectedVersion, &current); err != nil {
		return model.Specimen{}, fmt.Errorf("update 检验样本: %w", err)
	}
	_ = s.security.Audit(ctx, actor, requestID, "update", "Specimen", id, current.Status, current.Status, "updated business fields")
	return s.Get(ctx, id)
}

func (s *specimenService) Transition(ctx context.Context, id uint, input dto.TransitionRequest, actor, requestID string) (model.Specimen, error) {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.Specimen{}, err
	}
	target := strings.TrimSpace(input.Status)
	if !constants.CanTransition(constants.SpecimenTransitions, current.Status, target) {
		return model.Specimen{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current.Status, target)
	}
	if target == "disposed" {
		hasOpenChildren, err := s.repository.HasUndisposedChildren(ctx, id)
		if err != nil {
			return model.Specimen{}, err
		}
		if hasOpenChildren {
			return model.Specimen{}, fmt.Errorf("%w: %s", ErrUndisposedChildren, current.Code)
		}
	}
	before := current.Status
	current.Status = target
	current.Version = input.ExpectedVersion + 1
	current.UpdatedAt = time.Now().UTC()
	if err := s.repository.Update(ctx, id, input.ExpectedVersion, &current); err != nil {
		return model.Specimen{}, fmt.Errorf("transition 检验样本: %w", err)
	}
	if err := s.security.Audit(ctx, actor, requestID, "transition", "Specimen", id, before, target, input.Reason); err != nil {
		return model.Specimen{}, fmt.Errorf("persist transition audit: %w", err)
	}
	return s.Get(ctx, id)
}

// Split performs the atomic 样本分装 batch. The mother keeps its state while its
// available amount is debited; every child gets a continuous, mother-traceable
// code and one provenance row. Any validation, amount or concurrency failure
// rejects the whole batch without touching the mother.
func (s *specimenService) Split(ctx context.Context, id uint, input dto.SplitSpecimen, actor, requestID string) (model.Specimen, error) {
	mother, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.Specimen{}, err
	}
	if !constants.CanSplitSpecimen(mother.Status) {
		return model.Specimen{}, fmt.Errorf("%w: %s", ErrSplitNotAllowed, mother.Status)
	}
	if mother.Version != input.ExpectedVersion {
		return model.Specimen{}, repository.ErrVersionConflict
	}
	if len(input.Portions) < 2 || len(input.Portions) > 5 {
		return model.Specimen{}, fmt.Errorf("%w: split requires 2 to 5 portions", ErrInvalidInput)
	}
	var total float64
	for _, portion := range input.Portions {
		if portion.Amount <= 0 || math.IsNaN(portion.Amount) || math.IsInf(portion.Amount, 0) {
			return model.Specimen{}, fmt.Errorf("%w: each portion amount must be positive", ErrInvalidInput)
		}
		total += portion.Amount
	}
	if greaterFloat(total, mother.AvailableAmount) {
		return model.Specimen{}, fmt.Errorf("%w: requested %g but only %g %s remains",
			ErrInsufficientAmount, total, mother.AvailableAmount, mother.AvailableUnit)
	}

	now := time.Now().UTC()
	existing := uint(len(mother.Children))
	count := uint(len(input.Portions))
	children := make([]model.Specimen, 0, count)
	splits := make([]model.SpecimenSplit, 0, count)
	for index, portion := range input.Portions {
		sequence := existing + uint(index) + 1
		child := model.Specimen{
			BaseModel: model.BaseModel{
				Code: fmt.Sprintf("%s-%02d", mother.Code, sequence),
				Name: fmt.Sprintf("%s-分装子样%02d", mother.Name, sequence),
				// Children always start as received regardless of the mother's
				// testing state; their lifecycle then follows the normal graph.
				Status:      model.SpecimenInitialStatus,
				Version:     1,
				Description: strings.TrimSpace(fmt.Sprintf("由母样 %s 第 %d 次分装生成；%s", mother.Code, sequence, input.Reason)),
			},
			Facility: mother.Facility, Owner: mother.Owner, Category: mother.Category,
			RiskLevel: mother.RiskLevel, MetricValue: mother.MetricValue, MetricUnit: mother.MetricUnit,
			EffectiveAt: now, Evidence: mother.Evidence,
			RelatedCode:     mother.Code,
			AvailableAmount: portion.Amount, AvailableUnit: mother.AvailableUnit,
			SplitSeq: sequence,
		}
		parentID := mother.ID
		child.ParentID = &parentID
		children = append(children, child)

		splits = append(splits, model.SpecimenSplit{
			ParentID: mother.ID, Sequence: sequence, ChildCode: child.Code,
			Amount: portion.Amount, AmountUnit: mother.AvailableUnit,
			ParentRemaining: mother.AvailableAmount - total,
			ExpectedVersion: input.ExpectedVersion, ResultingVersion: input.ExpectedVersion + 1,
			Actor: actor, RequestID: requestID, Reason: strings.TrimSpace(input.Reason),
			CreatedAt: now,
		})
	}

	updated := mother
	updated.Version = input.ExpectedVersion + 1
	updated.UpdatedAt = now
	if err := s.repository.Split(ctx, &updated, total, children, splits, actor, requestID); err != nil {
		return model.Specimen{}, fmt.Errorf("split 检验样本: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *specimenService) Delete(ctx context.Context, id uint, actor, requestID string) error {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repository.Delete(ctx, id); err != nil {
		return err
	}
	return s.security.Audit(ctx, actor, requestID, "delete", "Specimen", id, current.Status, "deleted", "soft deleted 检验样本")
}

func (s *specimenService) StatusCounts(ctx context.Context) (map[string]int64, error) {
	return s.repository.CountByStatus(ctx)
}

// decorateBlockedReason assembles the human-readable reason that explains why a
// write is currently blocked. The state machine and child checks stay enforced
// server-side; the text only supports the specimen page.
func (s *specimenService) decorateBlockedReason(_ context.Context, item *model.Specimen) {
	if open := countUndisposed(item.Children); open > 0 {
		item.BlockedReason = fmt.Sprintf("尚有 %d 份未处置子样，母样不可处置", open)
		return
	}
	switch {
	case item.Status == "disposed":
		item.BlockedReason = "样本已处置，为终态"
	case constants.CanSplitSpecimen(item.Status):
		item.BlockedReason = ""
	default:
		item.BlockedReason = fmt.Sprintf("当前状态 %s 不允许分装，仅已接收/检测中可分装", item.Status)
	}
}

func countUndisposed(children []model.SpecimenLineage) int {
	count := 0
	for _, child := range children {
		if child.Status != "disposed" {
			count++
		}
	}
	return count
}

func greaterFloat(a, b float64) bool { return a-b > 1e-9 }

func validateSpecimenBusinessFields(code, name, facility, owner string) error {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(facility) == "" || strings.TrimSpace(owner) == "" {
		return ErrInvalidInput
	}
	return nil
}
