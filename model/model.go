package model

import (
	"time"

	"gorm.io/gorm"
)

func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&MessageMetaData{},
		&Mailbox{},
	)
}

type Model struct {
	ID        uint64          `gorm:"primaryKey;autoIncrement:true" json:"id"`
	CreatedAt time.Time       `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time       `gorm:"autoUpdateTime" json:"updated_at"`
	DeletedAt *gorm.DeletedAt `gorm:"autoDeleteTime" json:"-"`
}
