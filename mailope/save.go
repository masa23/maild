package mailope

import (
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/mail"
	"strconv"
	"strings"

	"github.com/masa23/maild/mailparser"
	"github.com/masa23/maild/model"
	"gorm.io/gorm"
)

func hasAttachments(msg *mail.Message) (bool, error) {
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		return false, err
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(msg.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				if err == io.EOF {
					break
				}
				return false, err
			}
			cd := p.Header.Get("Content-Disposition")
			if strings.HasPrefix(cd, "attachment") || strings.Contains(cd, "filename=") {
				return true, nil
			}
		}
	}
	return false, nil
}

func SaveMetaData(user string, r io.Reader, objKey string, size int64, db *gorm.DB, mailboxID uint64) error {
	msg, err := mail.ReadMessage(r)
	if err != nil {
		log.Fatalf("Error reading message: %v", err)
	}

	// Date to time.Time conversion
	timestamp, _ := msg.Header.Date()

	hasAttachments, err := hasAttachments(msg)
	if err != nil {
		return fmt.Errorf("error checking for attachments: %v", err)
	}

	// Subject decoding
	subject := msg.Header.Get("Subject")
	decodedSubject, err := mailparser.DecodeHeader(subject)
	if err != nil {
		log.Printf("Error decoding subject=%s err=%v", subject, err)
		decodedSubject = subject // Fallback to original subject if decoding fails
	}
	decodedFrom, err := mailparser.DecodeHeader(msg.Header.Get("From"))
	if err != nil {
		log.Printf("Error decoding From header: %v", err)
		decodedFrom = msg.Header.Get("From") // Fallback to original From if decoding fails
	}
	decodedTo, err := mailparser.DecodeHeader(msg.Header.Get("To"))
	if err != nil {
		log.Printf("Error decoding To header: %v", err)
		decodedTo = msg.Header.Get("To") // Fallback to original To if decoding fails
	}
	decodedCc, err := mailparser.DecodeHeader(msg.Header.Get("Cc"))
	if err != nil {
		log.Printf("Error decoding Cc header: %v", err)
		decodedCc = msg.Header.Get("Cc") // Fallback to original Cc if decoding fails
	}
	decodedBcc, err := mailparser.DecodeHeader(msg.Header.Get("Bcc"))
	if err != nil {
		log.Printf("Error decoding Bcc header: %v", err)
		decodedBcc = msg.Header.Get("Bcc") // Fallback to original Bcc if decoding fails
	}

	// SPAM detection
	// Extract X-Vade-Spamscore and mark as SPAM if 500 or higher and less than 9999
	spamScore := msg.Header.Get("X-Vade-Spamscore")
	score, err := strconv.Atoi(spamScore)
	if err != nil {
		log.Printf("Error converting spam score to integer: %v", err)
	}

	// Start transaction
	tx := db.Begin()
	if score >= 500 && score <= 9999 {
		log.Printf("SPAM detected with score: %d from msg ID: %s", score, msg.Header.Get("Message-ID"))
		// Save to SPAM folder instead of INBOX if it's SPAM
		var spamMailbox model.Mailbox
		// Get ID of SPAM folder
		if err := tx.Where("user = ? AND name = ?", msg.Header.Get("Delivered-To"), "SPAM").First(&spamMailbox).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				// Create SPAM folder if it doesn't exist
				spamMailbox = model.Mailbox{
					Name: "SPAM",
					User: msg.Header.Get("Delivered-To"),
				}
				if err := tx.Create(&spamMailbox).Error; err != nil {
					tx.Rollback()
					return fmt.Errorf("error creating SPAM mailbox: %v", err)
				}
			} else {
				tx.Rollback()
				return fmt.Errorf("error finding SPAM mailbox: %v", err)
			}
		}
		mailboxID = spamMailbox.ID // Use SPAM folder ID
	}

	if user == "" {
		user = msg.Header.Get("Delivered-To")
		if user == "" {
			log.Fatalf("No user specified and no Delivered-To header found")
		}
	}

	metaData := model.MessageMetaData{
		User:             user,
		Subject:          decodedSubject,
		From:             decodedFrom,
		To:               decodedTo,
		Cc:               decodedCc,
		Bcc:              decodedBcc,
		Size:             size,
		Timestamp:        timestamp,
		HasAttachments:   hasAttachments,
		Flags:            []string{},
		Headers:          msg.Header,
		MailboxID:        mailboxID,
		ObjectStorageKey: objKey,
	}

	if err := tx.Create(&metaData).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("error saving metadata to database: %v", err)
	}
	log.Printf("Metadata saved with ID: %d", metaData.ID)

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("error committing transaction: %v", err)
	}

	return nil
}
