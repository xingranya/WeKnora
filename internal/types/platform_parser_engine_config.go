package types

import "time"

// PlatformParserEngineConfig 保存平台统一维护的解析引擎连接配置。
// 该配置不属于任何空间，只能由系统管理员通过专用接口维护。
type PlatformParserEngineConfig struct {
	ID             uint64              `gorm:"primaryKey" json:"id"`
	Config         *ParserEngineConfig `gorm:"type:jsonb;not null" json:"config"`
	LastModifiedBy string              `gorm:"type:varchar(36);not null;default:''" json:"last_modified_by"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

// TableName 固定平台解析配置表名。
func (PlatformParserEngineConfig) TableName() string {
	return "platform_parser_engine_configs"
}
