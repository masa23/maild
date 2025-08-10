package main

import (
	"errors"
	"flag"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/masa23/maild/config"
	"github.com/masa23/maild/model"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var (
	conf    *config.Config
	version = "dev"
	db      *gorm.DB
)

func inboxToSpam() error {
	messages := []model.MessageMetaData{}

	// INBOXの全件取得
	if err := db.Where("mailbox_id = ?", 0).Find(&messages).Error; err != nil {
		return err
	}

	for _, msg := range messages {
		// HeaderからSPAMスコアを取得
		scoreStr := msg.Headers["X-Vade-Spamscore"]
		if len(scoreStr) == 0 {
			log.Printf("No spam score found for message ID %d", msg.ID)
			continue
		}
		score, err := strconv.Atoi(scoreStr[0])
		if err != nil {
			log.Printf("Error converting spam score to integer: %v", err)
			continue
		}

		log.Printf("Processing message ID %d with score %d", msg.ID, score)

		if score >= 500 && score <= 9999 {
			log.Printf("Moving message ID %d with score %d to SPAM folder", msg.ID, score)

			// SPAMフォルダのIDを取得
			var spamMailbox model.Mailbox
			if err := db.Where("name = ? AND user = ?", "SPAM", msg.User).First(&spamMailbox).Error; errors.Is(err, gorm.ErrRecordNotFound) {
				// SPAMフォルダが存在しない場合は作成
				spamMailbox = model.Mailbox{
					Name: "SPAM",
					User: msg.User,
				}
				if err := db.Create(&spamMailbox).Error; err != nil {
					log.Printf("Error creating SPAM mailbox: %v", err)
					continue
				}
			} else if err != nil {
				log.Printf("Error finding SPAM mailbox: %v", err)
				continue
			}

			// メッセージのMailboxIDを更新
			msg.MailboxID = spamMailbox.ID
			if err := db.Save(&msg).Error; err != nil {
				log.Printf("Error updating message mailbox ID: %v", err)
				continue
			}
		}
	}

	return nil
}

func main() {
	var confPath string
	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "Show version")
	flag.StringVar(&confPath, "conf", "./config.yaml", "Path to the configuration file")
	flag.Parse()

	if showVersion {
		log.Printf("Version: %s", version)
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}

	dir := filepath.Dir(exePath)
	if err := os.Chdir(dir); err != nil {
		log.Fatal(err)
	}

	conf, err = config.Load(confPath)
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}

	// Initialize database connection
	db, err = gorm.Open(mysql.Open(conf.Database), &gorm.Config{})
	if err != nil {
		log.Fatalf("Error connecting to database: %v", err)
	}

	if err := model.Migrate(db); err != nil {
		log.Fatalf("Error migrating database: %v", err)
	}

	if err := inboxToSpam(); err != nil {
		log.Fatalf("Error moving messages from INBOX to SPAM: %v", err)
	}
}
