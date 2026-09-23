package service

import (
	"context"
	"errors"
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
	Split(context.Context, uint, dto.SplitSpecimen, string, string) (model.Specimen, []model.Specimen, error)
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
	return s.repository.List(ctx, query)
}

func (s *specimenService) Get(ctx context.Context, id uint) (model.Specimen, error) {
	return s.repository.Get(ctx, id)
}

func (s *specimenService) Create(ctx context.Context, input dto.CreateSpecimen, actor, requestID string) (model.Specimen, error) {
	if err := validateSpecimenBusinessFields(input.Code, input.Name, input.Facility, input.Owner); err != nil {
		return model.Specimen{}, err
	}
	quantity := normalizeQuantity(input.Quantity)
	item := model.Specimen{
		BaseModel: model.BaseModel{
			Code: strings.ToUpper(strings.TrimSpace(input.Code)), Name: strings.TrimSpace(input.Name),
			Status: model.SpecimenInitialStatus, Version: 1, Description: strings.TrimSpace(input.Description),
		},
		Facility: strings.TrimSpace(input.Facility), Owner: strings.TrimSpace(input.Owner),
		Category: strings.TrimSpace(input.Category), RiskLevel: input.RiskLevel,
		MetricValue: input.MetricValue, MetricUnit: strings.TrimSpace(input.MetricUnit),
		EffectiveAt: input.EffectiveAt.UTC(), Evidence: strings.TrimSpace(input.Evidence),
		RelatedCode: strings.ToUpper(strings.TrimSpace(input.RelatedCode)),
		// 新接收样本的可用量等于初始入库量。
		Quantity: quantity, AvailableQuantity: quantity,
	}
	if err := s.repository.Create(ctx, &item); err != nil {
		return model.Specimen{}, fmt.Errorf("create 检验样本: %w", err)
	}
	_ = s.security.Audit(ctx, actor, requestID, "create", "Specimen", item.ID, "", item.Status, "created 检验样本")
	return s.repository.Get(ctx, item.ID)
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
	current.Version = input.ExpectedVersion + 1
	current.UpdatedAt = time.Now().UTC()
	if err := s.repository.Update(ctx, id, input.ExpectedVersion, &current); err != nil {
		return model.Specimen{}, fmt.Errorf("update 检验样本: %w", err)
	}
	_ = s.security.Audit(ctx, actor, requestID, "update", "Specimen", id, current.Status, current.Status, "updated business fields")
	return s.repository.Get(ctx, id)
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
	// 母样还有未处置子样时不得处置母样。
	if target == string(constants.SpecimenStateDisposed) {
		pending, countErr := s.repository.CountPendingChildren(ctx, id)
		if countErr != nil {
			return model.Specimen{}, countErr
		}
		if pending > 0 {
			return model.Specimen{}, fmt.Errorf("%w: 母样 %s 尚有 %d 份未处置子样", ErrInvalidInput, current.Code, pending)
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
	return s.repository.Get(ctx, id)
}

const (
	specimenSplitMinParts = 2
	specimenSplitMaxParts = 5
	quantityEpsilon       = 1e-9
)

// Split 执行一次样本分装：校验状态与余量后，由仓储在单一事务内扣减母样余量并
// 生成二至五份连续编号、可追溯的子样。任何失败都整批拒绝且母样不变。
func (s *specimenService) Split(ctx context.Context, id uint, input dto.SplitSpecimen, actor, requestID string) (model.Specimen, []model.Specimen, error) {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.Specimen{}, nil, err
	}
	token := strings.TrimSpace(input.ClientToken)
	if len(token) < 8 || len(token) > 64 {
		return model.Specimen{}, nil, ErrInvalidInput
	}
	if len(input.Parts) < specimenSplitMinParts || len(input.Parts) > specimenSplitMaxParts {
		return model.Specimen{}, nil, fmt.Errorf("%w: 分装份数必须为 %d-%d 份", ErrInvalidInput, specimenSplitMinParts, specimenSplitMaxParts)
	}

	// 重复提交最先判定：无论母样当前余量或版本如何，同一批次令牌一律整批拒绝且母样不变。
	duplicate, err := s.repository.SplitExistsByToken(ctx, token)
	if err != nil {
		return model.Specimen{}, nil, err
	}
	if duplicate {
		return model.Specimen{}, nil, fmt.Errorf("%w: 该分装批次已提交，请勿重复操作", repository.ErrDuplicateSplit)
	}

	// 状态不允许：仅已接收或检测中的样本可分装。
	if !constants.SpecimenSplitStates[current.Status] {
		return model.Specimen{}, nil, fmt.Errorf("%w: 状态 %s 不允许分装", ErrInvalidInput, current.Status)
	}

	parts := make([]float64, 0, len(input.Parts))
	total := 0.0
	for index, raw := range input.Parts {
		part := normalizeQuantity(raw)
		if part <= 0 {
			return model.Specimen{}, nil, fmt.Errorf("%w: 第 %d 份分配量必须大于 0", ErrInvalidInput, index+1)
		}
		parts = append(parts, part)
		total += part
	}

	// 余量不足：整批拒绝，母样余量不变。
	if total > current.AvailableQuantity+quantityEpsilon {
		return model.Specimen{}, nil, fmt.Errorf("%w: 申请分装 %v%s，剩余可用量仅 %v%s",
			repository.ErrInsufficientQuantity, trimSplitQuantity(total), unitText(current.QuantityUnit),
			trimSplitQuantity(current.AvailableQuantity), unitText(current.QuantityUnit))
	}

	// 子样连续编号：同一母样下从既有最大序号之后连续生成。
	var maxSequence uint
	for _, child := range current.Children {
		if child.SplitSequence > maxSequence {
			maxSequence = child.SplitSequence
		}
	}
	now := time.Now().UTC()
	children := make([]model.Specimen, 0, len(parts))
	for index, part := range parts {
		sequence := maxSequence + uint(index+1)
		child := model.Specimen{
			BaseModel: model.BaseModel{
				Code:   fmt.Sprintf("%s-C%02d", current.Code, sequence),
				Name:   fmt.Sprintf("%s 子样%02d", strings.TrimSuffix(current.Name, " "), sequence),
				Status: model.SpecimenInitialStatus, Version: 1,
				Description: fmt.Sprintf("由母样 %s 第 %d 次分装产生（%s）", current.Code, sequence, strings.TrimSpace(input.Reason)),
				CreatedAt:   now, UpdatedAt: now,
			},
			Facility: current.Facility, Owner: actor,
			Category: current.Category, RiskLevel: current.RiskLevel,
			MetricUnit: current.MetricUnit, EffectiveAt: now,
			Evidence:    fmt.Sprintf("来源母样 %s；%s", current.Code, strings.TrimSpace(current.Evidence)),
			RelatedCode: current.Code,
			// 子样初始量即本份分配量；后续子样还可继续分装。
			Quantity: part, AvailableQuantity: part, QuantityUnit: current.QuantityUnit,
			ParentID: current.ID, SplitSequence: sequence, SplitToken: token,
		}
		children = append(children, child)
	}

	plan := repository.SplitPlan{
		Parent: current, ExpectedVersion: input.ExpectedVersion,
		Token: token, Parts: parts, Children: children, Reason: strings.TrimSpace(input.Reason),
	}
	parent, created, err := s.repository.Split(ctx, plan, actor, requestID)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateSplit) {
			return model.Specimen{}, nil, fmt.Errorf("%w: 该分装批次已提交，请勿重复操作", err)
		}
		return model.Specimen{}, nil, err
	}
	return parent, created, nil
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

func validateSpecimenBusinessFields(code, name, facility, owner string) error {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(facility) == "" || strings.TrimSpace(owner) == "" {
		return ErrInvalidInput
	}
	return nil
}

// normalizeQuantity 统一为 4 位小数，避免浮点尾差导致余量判断不稳定。
func normalizeQuantity(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Round(value*10000) / 10000
}

func trimSplitQuantity(value float64) float64 {
	return normalizeQuantity(value)
}

func unitText(unit string) string {
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return ""
	}
	return " " + unit
}
