package model

import (
	"time"
)

type MessageMetaData struct {
	Model
	User             string              `gorm:"type:varchar(255);not null; index" json:"user"`
	Subject          string              `gorm:"type:text;not null" json:"subject"`
	From             string              `gorm:"type:text;not null" json:"from"`
	To               string              `gorm:"type:text;not null" json:"to"`
	Cc               string              `gorm:"type:text;not null" json:"cc"`
	Bcc              string              `gorm:"type:text;not null" json:"bcc"`
	Size             int64               `gorm:"not null" json:"size"`
	Timestamp        time.Time           `gorm:"not null" json:"timestamp"`
	HasAttachments   bool                `gorm:"not null" json:"has_attachments"`
	Flags            []string            `gorm:"type:json;serializer:json;not null" json:"flags"`
	Headers          map[string][]string `gorm:"type:json;serializer:json;not null" json:"headers"`
	ObjectStorageKey string              `gorm:"type:varchar(512);not null" json:"object_storage_key"`
	// Mailbox ID 0 is reserved for INBOX
	MailboxID uint64 `gorm:"not null; index" json:"mailbox_id"`
}

type Mailbox struct {
	Model
	Name string `gorm:"type:varchar(255);not null; index" json:"name"`
	User string `gorm:"type:varchar(255);not null; index" json:"user"`
}
