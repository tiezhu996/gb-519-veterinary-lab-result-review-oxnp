package model

import "time"

// Specimen models 检验样本 as an independently versioned aggregate. The fields
// cover ownership, operational context, evidence and measured risk so later
// changes naturally span persistence, service and UI layers.
//
// 分装（aliquot）字段让一个母样可以把 AvailableQuantity 扣减成二至五个连续编号的
// 子样。子样通过 ParentID/SplitSequence/SplitToken 追溯到母样与同一分装批次。
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

	// Quantity 是接收入库时的初始可分装量；AvailableQuantity 是分装扣减后的剩余可用量。
	Quantity          float64 `json:"quantity" gorm:"not null;default:0"`
	QuantityUnit      string  `json:"quantityUnit" gorm:"size:24"`
	AvailableQuantity float64 `json:"availableQuantity" gorm:"not null;default:0"`

	// ParentID 非空表示该样本是某次分装产生的子样；根样本（母样）为 0。
	ParentID uint `json:"parentId" gorm:"index"`
	// SplitSequence 是同一母样下子样的连续序号，从 1 开始，0 表示非子样。
	SplitSequence uint `json:"splitSequence"`
	// SplitToken 是一次分装批次的幂等键，母样与本批全部子样共享同一个值。
	SplitToken string `json:"splitToken" gorm:"size:64;index"`

	// 以下字段不持久化，由仓储在读取时统一补齐，用于样本页展示来源与阻断原因。
	ParentCode   string          `json:"parentCode,omitempty" gorm:"-"`
	Children     []SpecimenChild `json:"children,omitempty" gorm:"-"`
	BlockReasons []string        `json:"blockReasons,omitempty" gorm:"-"`
}

// SpecimenChild 是样本清单中子样的只读摘要，保持连续编号与可追溯信息。
type SpecimenChild struct {
	ID                uint    `json:"id"`
	Code              string  `json:"code"`
	Name              string  `json:"name"`
	Status            string  `json:"status"`
	AvailableQuantity float64 `json:"availableQuantity"`
	QuantityUnit      string  `json:"quantityUnit"`
	SplitSequence     uint    `json:"splitSequence"`
	Disposed          bool    `json:"disposed"`
}

func (item *Specimen) GetBase() *BaseModel { return &item.BaseModel }

func (item Specimen) TableName() string { return "specimens" }

var SpecimenInitialStatus = "received"

// IsChild 报告该样本是否为分装产生的子样。
func (item *Specimen) IsChild() bool { return item.ParentID != 0 }

// SpecimenSplit 记录一次成功的分装批次，是并发与重复提交的幂等锚点。
type SpecimenSplit struct {
	ID         uint      `json:"id" gorm:"primaryKey"`
	Token      string    `json:"token" gorm:"size:64;uniqueIndex;not null"`
	ParentID   uint      `json:"parentId" gorm:"index;not null"`
	ParentCode string    `json:"parentCode" gorm:"size:64;not null"`
	Count      int       `json:"count" gorm:"not null"`
	Total      float64   `json:"total"`
	Actor      string    `json:"actor" gorm:"size:80;index;not null"`
	RequestID  string    `json:"requestId" gorm:"size:64;index;not null"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (SpecimenSplit) TableName() string { return "specimen_splits" }
