package model

import "time"

// Specimen models 检验样本 as an independently versioned aggregate. The fields
// cover ownership, operational context, evidence and measured risk so later
// changes naturally span persistence, service and UI layers.
type Specimen struct {
	BaseModel
	Facility    string    `json:"facility" gorm:"size:120;index"`
	Owner       string    `json:"owner" gorm:"size:120;index"`
	Category    string    `json:"category" gorm:"size:80;index"`
	RiskLevel   string    `json:"riskLevel" gorm:"size:32;index"`
	MetricValue float64   `json:"metricValue"`
	MetricUnit  string    `json:"metricUnit" gorm:"size:24"`
	EffectiveAt time.Time `json:"effectiveAt"`
	Evidence    string    `json:"evidence" gorm:"size:2000"`
	RelatedCode string    `json:"relatedCode" gorm:"size:64;index"`

	// AvailableAmount is the remaining usable quantity after every accepted
	// split. AvailableUnit carries its measurement unit.
	AvailableAmount float64 `json:"availableAmount"`
	AvailableUnit   string  `json:"availableUnit" gorm:"size:24"`
	// ParentID points at the mother specimen for children produced by 分装;
	// SplitSeq is the child ordinal within its mother and stays 0 for mothers.
	ParentID *uint `json:"parentId,omitempty" gorm:"index"`
	SplitSeq uint  `json:"splitSeq"`

	// Children and BlockedReason are read models assembled by the service and
	// are never stored as columns.
	Children      []SpecimenLineage `json:"children,omitempty" gorm:"-"`
	BlockedReason string            `json:"blockedReason,omitempty" gorm:"-"`
}

func (item *Specimen) GetBase() *BaseModel { return &item.BaseModel }

func (item Specimen) TableName() string { return "specimens" }

var SpecimenInitialStatus = "received"

// SpecimenSplit is the append-only provenance record for one accepted 样本分装
// batch. One row is written per child specimen, all sharing RequestID so a
// batch can always be traced back to the mother specimen.
type SpecimenSplit struct {
	ID               uint      `json:"id" gorm:"primaryKey"`
	ParentID         uint      `json:"parentId" gorm:"not null;uniqueIndex:idx_specimen_split_order,priority:1;index"`
	Sequence         uint      `json:"sequence" gorm:"not null;uniqueIndex:idx_specimen_split_order,priority:2"`
	ChildID          uint      `json:"childId" gorm:"not null;uniqueIndex"`
	ChildCode        string    `json:"childCode" gorm:"size:64;not null;index"`
	Amount           float64   `json:"amount" gorm:"not null"`
	AmountUnit       string    `json:"amountUnit" gorm:"size:24"`
	ParentRemaining  float64   `json:"parentRemaining" gorm:"not null"`
	ExpectedVersion  uint      `json:"expectedVersion" gorm:"not null"`
	ResultingVersion uint      `json:"resultingVersion" gorm:"not null"`
	Actor            string    `json:"actor" gorm:"size:80;not null;index"`
	RequestID        string    `json:"requestId" gorm:"size:64;not null;index"`
	Reason           string    `json:"reason" gorm:"size:500"`
	CreatedAt        time.Time `json:"createdAt" gorm:"index"`
}

func (SpecimenSplit) TableName() string { return "specimen_splits" }

// SpecimenLineage is the compact child view embedded in specimen responses.
type SpecimenLineage struct {
	ID              uint    `json:"id"`
	Code            string  `json:"code"`
	Name            string  `json:"name"`
	Status          string  `json:"status"`
	Version         uint    `json:"version"`
	Sequence        uint    `json:"sequence"`
	AvailableAmount float64 `json:"availableAmount"`
	AvailableUnit   string  `json:"availableUnit"`
	Amount          float64 `json:"amount"`
	ParentID        uint    `json:"parentId"`
}
