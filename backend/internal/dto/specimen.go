package dto

import "time"

// CreateSpecimen is the public write contract for 检验样本. Status is deliberately
// omitted so callers cannot bypass the service state machine.
type CreateSpecimen struct {
	Code            string    `json:"code" binding:"required,min=2,max=64"`
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
	AvailableAmount float64   `json:"availableAmount" binding:"gte=0"`
	AvailableUnit   string    `json:"availableUnit" binding:"max=24"`
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
	AvailableAmount float64   `json:"availableAmount" binding:"gte=0"`
	AvailableUnit   string    `json:"availableUnit" binding:"max=24"`
}

// SplitSpecimenPortion is one child portion of a 样本分装 batch.
type SplitSpecimenPortion struct {
	Amount float64 `json:"amount" binding:"gt=0"`
}

// SplitSpecimen is the atomic 样本分装 contract: the mother keeps its status,
// its available amount is debited by the sum of portions and 2-5 children are
// generated with continuous, mother-traceable codes.
type SplitSpecimen struct {
	ExpectedVersion uint                   `json:"expectedVersion" binding:"required"`
	Reason          string                 `json:"reason" binding:"required,min=3,max=500"`
	Portions        []SplitSpecimenPortion `json:"portions" binding:"required,min=2,max=5,dive"`
}
