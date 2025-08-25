package imapsession

import (
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/masa23/maild/config"
	"gorm.io/gorm"
)

// Global variables initialized by the main function
var (
	db       *gorm.DB
	s3Client *s3.S3
	conf     *config.Config
)

func Init(database *gorm.DB, s3client *s3.S3, configuration *config.Config) {
	db = database
	s3Client = s3client
	conf = configuration
}

type Session struct {
	username  string
	mailbox   string
	mailboxID uint64
}

func (s *Session) SupportsIMAP4rev2() bool {
	return true
}
