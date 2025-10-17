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
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/masa23/maild/mailparser"
	"github.com/masa23/maild/model"
	"gorm.io/gorm"
)

const (
	SpamScoreMin = 100
	SpamScoreMax = 9999
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

func sanitizeString(s string) string {
	// Remove or replace invalid UTF-8 sequences
	if !utf8.ValidString(s) {
		v := make([]rune, 0, len(s))
		for i, r := range s {
			if r == utf8.RuneError {
				_, size := utf8.DecodeRuneInString(s[i:])
				if size == 1 {
					// Invalid byte sequence, skip it
					continue
				}
			}
			v = append(v, r)
		}
		s = string(v)
	}

	// Remove control characters except tab and newline
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' {
			return r
		}
		if unicode.IsControl(r) {
			return -1 // Remove control characters
		}
		return r
	}, s)
}

func decodeHeaderField(header mail.Header, field string) string {
	value := header.Get(field)
	decodedValue, err := mailparser.DecodeHeader(value)
	if err != nil {
		log.Printf("Error decoding %s header: %v", field, err)
		return sanitizeString(value) // Fallback to original value if decoding fails
	}
	return sanitizeString(decodedValue)
}

func createSpamFolder(tx *gorm.DB, user string) (uint64, error) {
	var spamMailbox model.Mailbox
	// Get ID of SPAM folder
	if err := tx.Where("user = ? AND name = ?", user, "SPAM").First(&spamMailbox).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			// Create SPAM folder if it doesn't exist
			spamMailbox = model.Mailbox{
				Name: "SPAM",
				User: user,
			}
			if err := tx.Create(&spamMailbox).Error; err != nil {
				return 0, fmt.Errorf("error creating SPAM mailbox: %v", err)
			}
		} else {
			return 0, fmt.Errorf("error finding SPAM mailbox: %v", err)
		}
	}
	return spamMailbox.ID, nil
}

func SaveMetaData(user string, r io.Reader, objKey string, size int64, db *gorm.DB, mailboxID uint64) error {
	msg, err := mail.ReadMessage(r)
	if err != nil {
		log.Printf("Error reading message: %v", err)
		return fmt.Errorf("error reading message: %v", err)
	}

	// Date to time.Time conversion
	timestamp, err := msg.Header.Date()
	if err != nil {
		log.Printf("Error parsing date header: %v, using current time as fallback", err)
		timestamp = time.Now()
	}

	hasAttachments, err := hasAttachments(msg)
	if err != nil {
		return fmt.Errorf("error checking for attachments: %v", err)
	}

	// Decode header fields
	decodedSubject := decodeHeaderField(msg.Header, "Subject")
	decodedFrom := decodeHeaderField(msg.Header, "From")
	decodedTo := decodeHeaderField(msg.Header, "To")
	decodedCc := decodeHeaderField(msg.Header, "Cc")
	decodedBcc := decodeHeaderField(msg.Header, "Bcc")

	// SPAM detection
	// Extract X-Vade-Spamscore and mark as SPAM if within defined range
	spamScore := msg.Header.Get("X-Vade-Spamscore")
	score, err := strconv.Atoi(spamScore)
	if err != nil {
		log.Printf("Error converting spam score to integer: %v", err)
	}

	// Start transaction
	tx := db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	if score >= SpamScoreMin && score <= SpamScoreMax {
		log.Printf("SPAM detected with score: %d from msg ID: %s", score, msg.Header.Get("Message-ID"))
		// Save to SPAM folder instead of INBOX if it's SPAM
		spamMailboxID, err := createSpamFolder(tx, msg.Header.Get("Delivered-To"))
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("error creating SPAM folder: %v", err)
		}
		mailboxID = spamMailboxID // Use SPAM folder ID
	}

	if user == "" {
		user = msg.Header.Get("Delivered-To")
		if user == "" {
			log.Printf("No user specified and no Delivered-To header found")
			tx.Rollback()
			return fmt.Errorf("no user specified and no Delivered-To header found")
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
