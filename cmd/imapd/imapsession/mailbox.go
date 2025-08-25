package imapsession

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/k0kubun/pp/v3"
	"github.com/masa23/maild/model"
	"gorm.io/gorm"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

// List implements the IMAP LIST command
func (s *Session) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	log.Println(pp.Sprintf("List called with ref: %s, patterns: %v, options: %v", ref, patterns, options))

	// Get mailboxes
	var mailboxes []model.Mailbox
	if err := db.Where("user = ?", s.username).Find(&mailboxes).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	// Return list of mailboxes
	list := imap.ListData{
		Attrs:   []imap.MailboxAttr{imap.MailboxAttrHasNoChildren},
		Delim:   '/',
		Mailbox: "INBOX",
	}

	if err := w.WriteList(&list); err != nil {
		return err
	}
	for _, mailbox := range mailboxes {
		if err := w.WriteList(&imap.ListData{
			Attrs:   []imap.MailboxAttr{imap.MailboxAttrHasNoChildren},
			Delim:   '/',
			Mailbox: mailbox.Name,
		}); err != nil {
			return err
		}
	}

	return nil
}

// Select implements the IMAP SELECT command
func (s *Session) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	log.Println(pp.Printf("Select called with mailbox: %s, options: %v", mailbox, options))
	// Mailbox selection process
	var mailboxID uint64

	// Currently only supports INBOX
	if strings.ToUpper(mailbox) == "INBOX" {
		// Set MailBoxID to 0 only for INBOX
		mailboxID = 0
	} else {
		// For other mailboxes, get the ID
		var mbox model.Mailbox
		if err := db.Where("name = ? AND user = ?", mailbox, s.username).First(&mbox).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, &imap.Error{
					Type: imap.StatusResponseTypeNo,
					Text: "Mailbox does not exist",
				}
			}
			return nil, fmt.Errorf("error finding mailbox: %v", err)
		}
		mailboxID = mbox.ID
	}

	// Get mailbox metadata
	var metaData model.MessageMetaData
	if err := db.Where(
		"user = ? AND mailbox_id = ?",
		s.username,
		mailboxID,
	).Last(&metaData).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	// Count messages in the mailbox
	var count int64
	if err := db.Model(&model.MessageMetaData{}).Where(
		"user = ? AND mailbox_id = ?",
		s.username,
		mailboxID,
	).Count(&count).Error; err != nil {
		return nil, err
	}
	// Count unread messages in the mailbox
	var unseenCount int64
	if err := db.Model(&model.MessageMetaData{}).Where(
		"user = ? AND mailbox_id = ? AND JSON_CONTAINS(flags, ?) = 0",
		s.username,
		mailboxID,
		fmt.Sprintf("%q", imap.FlagSeen),
	).Count(&unseenCount).Error; err != nil {
		return nil, err
	}

	// Dummy data: actual data should be retrieved from storage
	numMessages := uint32(count)
	uidNext := imap.UID(metaData.ID) + 1 // UID is assumed to be ID+1
	uidValidity := uint32(1)
	highestModSeq := uint64(123456)

	// Build basic info
	data := &imap.SelectData{
		Flags: []imap.Flag{
			imap.FlagSeen, imap.FlagDeleted, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDraft,
		},
		PermanentFlags: []imap.Flag{
			imap.FlagSeen, imap.FlagDeleted,
		},
		NumMessages: numMessages,
		UIDNext:     uidNext,
		UIDValidity: uidValidity,
	}

	if options != nil && options.CondStore {
		data.HighestModSeq = highestModSeq
	}

	// Set mailbox information
	s.mailbox = mailbox
	s.mailboxID = mailboxID

	return data, nil
}

// Create implements the IMAP CREATE command
func (s *Session) Create(mailbox string, options *imap.CreateOptions) error {
	log.Println(pp.Sprintf("Create called with mailbox: %s, options: %v", mailbox, options))
	if mailbox == "" {
		return &imap.Error{
			Type: imap.StatusResponseTypeBad,
			Text: "Mailbox name cannot be empty",
		}
	}

	// Create mailbox
	newMailbox := model.Mailbox{
		Name: mailbox,
		User: s.username,
	}

	if err := db.Create(&newMailbox).Error; err != nil {
		return fmt.Errorf("error creating mailbox: %v", err)
	}
	log.Printf("Mailbox created: %s", mailbox)

	return nil
}

// Delete implements the IMAP DELETE command
func (s *Session) Delete(mailbox string) error {
	log.Println(pp.Sprintf("Delete called with mailbox: %s", mailbox))
	if mailbox == "" {
		return &imap.Error{
			Type: imap.StatusResponseTypeBad,
			Text: "Mailbox name cannot be empty",
		}
	}

	// Delete mailbox
	var mailboxData model.Mailbox
	if err := db.Where("name = ? AND user = ?", mailbox, s.username).First(&mailboxData).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &imap.Error{
				Type: imap.StatusResponseTypeNo,
				Text: "Mailbox does not exist",
			}
		}
		return fmt.Errorf("error finding mailbox: %v", err)
	}
	if err := db.Delete(&mailboxData).Error; err != nil {
		return fmt.Errorf("error deleting mailbox: %v", err)
	}

	//ToDo: Delete messages associated with the mailbox

	log.Printf("Mailbox deleted: %s", mailbox)

	return nil
}

// Rename implements the IMAP RENAME command
func (s *Session) Rename(mailbox, newName string) error {
	log.Println(pp.Sprintf("Rename called with mailbox: %s, newName: %s", mailbox, newName))
	if mailbox == "" || newName == "" {
		return &imap.Error{
			Type: imap.StatusResponseTypeBad,
			Text: "Mailbox name and new name cannot be empty",
		}
	}

	// Get mailbox
	// Use transaction to ensure no duplicate mailbox names exist
	var mailboxData model.Mailbox
	tx := db.Begin()

	// Get existing mailbox
	if err := tx.Where("name = ? AND user = ?", mailbox, s.username).First(&mailboxData).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			tx.Rollback()
			return &imap.Error{
				Type: imap.StatusResponseTypeNo,
				Text: "Mailbox does not exist",
			}
		}
		tx.Rollback()
		return fmt.Errorf("error finding mailbox: %v", err)
	}

	// Check if new mailbox name already exists
	var existingMailbox model.Mailbox
	if err := tx.Where("name = ? AND user = ?", newName, s.username).First(&existingMailbox).Error; err == nil {
		tx.Rollback()
		return &imap.Error{
			Type: imap.StatusResponseTypeNo,
			Text: "A mailbox with the new name already exists",
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		tx.Rollback()
		return fmt.Errorf("error checking existing mailbox: %v", err)
	}

	// Update mailbox name
	mailboxData.Name = newName
	if err := tx.Save(&mailboxData).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("error renaming mailbox: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("transaction commit error: %v", err)
	}
	log.Printf("Mailbox name changed from %s to %s", mailbox, newName)

	// Update reference
	s.mailbox = newName
	s.mailboxID = mailboxData.ID

	return nil
}

// Subscribe implements the IMAP SUBSCRIBE command
func (s *Session) Subscribe(mailbox string) error {
	log.Println(pp.Sprintf("Subscribe called with mailbox: %s", mailbox))
	return nil
}

// Unsubscribe implements the IMAP UNSUBSCRIBE command
func (s *Session) Unsubscribe(mailbox string) error {
	log.Println(pp.Sprintf("Unsubscribe called with mailbox: %s", mailbox))
	return errors.New("Unsubscribe is not implemented")
}

// Status implements the IMAP STATUS command
func (s *Session) Status(mailbox string, options *imap.StatusOptions) (*imap.StatusData, error) {
	log.Println(pp.Sprintf("Status called with mailbox: %s, options: %v", mailbox, options))
	mailboxID := uint64(0) // ID of INBOX is 0
	if strings.ToUpper(mailbox) != "INBOX" {
		// For other mailboxes, get the ID
		var mbox model.Mailbox
		if err := db.Where("name = ? AND user = ?", mailbox, s.username).First(&mbox).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, &imap.Error{
					Type: imap.StatusResponseTypeNo,
					Text: "Mailbox does not exist",
				}
			}
			return nil, fmt.Errorf("error finding mailbox: %v", err)
		}
		mailboxID = mbox.ID
	}

	var msgs, unseen int64
	// Count messages
	db.Model(&model.MessageMetaData{}).Where(
		"user = ? AND mailbox_id = ?",
		s.username,
		mailboxID,
	).Count(&msgs)
	// Count unseen messages
	db.Model(&model.MessageMetaData{}).Where(
		"user = ? AND mailbox_id = ? AND JSON_CONTAINS(flags, ?) = 0",
		s.username,
		mailboxID,
		fmt.Sprintf("%q", imap.FlagSeen),
	).Count(&unseen)

	// ToDo: UIDNext handling is not correct.
	// For MySQL, ID=UID is fine, but for TiDB, it's not guaranteed to increment.
	var metaData model.MessageMetaData
	if err := db.Where(
		"user = ? AND mailbox_id = ?",
		s.username,
		mailboxID,
	).Last(&metaData).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("error finding last message metadata: %v", err)
	}

	numMessages := uint32(msgs)
	numUnseen := uint32(unseen)

	return &imap.StatusData{
		Mailbox:     mailbox,
		NumMessages: &numMessages,
		NumUnseen:   &numUnseen,
		UIDNext:     imap.UID(metaData.ID + 1), // UID is assumed to be ID+1
		UIDValidity: 1,
	}, nil
}
