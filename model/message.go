package model

import (
	"time"
)

type MessageMetaData struct {
	Model
	User             string              `gorm:"type:varchar(255);not null; index" json:"user"`
	Subject          string              `gorm:"type:text;not null" json:"subject"`
	FromRaw          string              `gorm:"type:text;not null" json:"from_raw"`
	From             string              `gorm:"type:text;not null" json:"from"`
	ToRaw            string              `gorm:"type:text;not null" json:"to_raw"`
	To               string              `gorm:"type:text;not null" json:"to"`
	CcRaw            string              `gorm:"type:text;not null" json:"cc_raw"`
	Cc               string              `gorm:"type:text;not null" json:"cc"`
	BccRaw           string              `gorm:"type:text;not null" json:"bcc_raw"`
	Bcc              string              `gorm:"type:text;not null" json:"bcc"`
	Size             int64               `gorm:"not null" json:"size"`
	MessageID        string              `gorm:"type:varchar(512);not null; index" json:"message_id"`
	References       string              `gorm:"type:text" json:"references"`
	Timestamp        time.Time           `gorm:"not null" json:"timestamp"`
	HasAttachments   bool                `gorm:"not null" json:"has_attachments"`
	Flags            []string            `gorm:"type:json;serializer:json;not null" json:"flags"`
	Headers          map[string][]string `gorm:"type:json;serializer:json;not null" json:"headers"`
	ObjectStorageKey string              `gorm:"type:varchar(512);not null" json:"object_storage_key"`
}
