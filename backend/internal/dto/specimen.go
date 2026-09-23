package dto

import (
	"time"

	"github.com/blueship581/veterinary-lab-result-review/backend/internal/model"
)

// CreateSpecimen is the public write contract for 检验样本. Status is deliberately
// omitted so callers cannot bypass the service state machine.
type CreateSpecimen struct {
	Code        string    `json:"code" binding:"required,min=2,max=64"`
	Name        string    `json:"name" binding:"required,min=2,max=160"`
	Description string    `json:"description" binding:"max=1000"`
	Facility    string    `json:"facility" binding:"required,max=120"`
	Owner       string    `json:"owner" binding:"required,max=120"`
	Category    string    `json:"category" binding:"required,max=80"`
	RiskLevel   string    `json:"riskLevel" binding:"required,oneof=low medium high critical"`
	MetricValue float64   `json:"metricValue"`
	MetricUnit  string    `json:"metricUnit" binding:"max=24"`
	EffectiveAt time.Time `json:"effectiveAt" binding:"required"`
	Evidence    string    `json:"evidence" binding:"max=2000"`
	RelatedCode string    `json:"relatedCode" binding:"max=64"`
	Quantity    float64   `json:"quantity" binding:"gte=0"`
}

type UpdateSpecimen struct {
	ExpectedVersion uint      `json:"expectedVersion" binding:"required"`
	Name            string    `json:"name" binding:"required,min=2,max=160"`
	Description     string    `json:"description" binding:"max=1000"`
	Facility        string    `json:"facility" binding:"required,max=120"`
	Owner           string    `json:"owner" binding:"required,max=120"`
	Category        string    `json:"category" binding:"required,max=80"`
	RiskLevel       string    `json:"riskLevel" binding:"required,oneof=low medium high critical"`
	MetricValue     float64   `json:"metricValue"`
	MetricUnit      string    `json:"metricUnit" binding:"max=24"`
	EffectiveAt     time.Time `json:"effectiveAt" binding:"required"`
	Evidence        string    `json:"evidence" binding:"max=2000"`
	RelatedCode     string    `json:"relatedCode" binding:"max=64"`
}

// SplitSpecimen 是样本分装的写入契约。Parts 为每份分配量（按顺序生成连续编号子样），
// ClientToken 由调用方生成并在重复提交时原样回传，服务端据此整批去重。
type SplitSpecimen struct {
	ExpectedVersion uint      `json:"expectedVersion" binding:"required"`
	Parts           []float64 `json:"parts" binding:"required,min=2,max=5,dive,gt=0"`
	Reason          string    `json:"reason" binding:"required,min=3,max=500"`
	ClientToken     string    `json:"clientToken" binding:"required,min=8,max=64"`
}

// SplitSpecimenResult 返回分装后的母样（含更新的可用量与子样清单）与本批新子样。
type SplitSpecimenResult struct {
	Parent   model.Specimen   `json:"parent"`
	Children []model.Specimen `json:"children"`
	Count    int              `json:"count"`
}
