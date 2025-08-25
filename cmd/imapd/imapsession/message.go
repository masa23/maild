package imapsession

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/mail"
	"strings"

	"github.com/k0kubun/pp/v3"
	"github.com/masa23/maild/mailope"
	"github.com/masa23/maild/mailparser"
	"github.com/masa23/maild/model"
	"github.com/masa23/maild/objectstorage"
	"gorm.io/gorm"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

// Append implements the IMAP APPEND command
func (s *Session) Append(mailbox string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	log.Println(pp.Sprintf("Append called with mailbox: %s, options: %v, session: %v", mailbox, options, s))
	var mailboxID uint64

	if mailbox == "" {
		return nil, &imap.Error{
			Type: imap.StatusResponseTypeBad,
			Text: "Mailbox name cannot be empty",
		}
	}

	if mailbox == "INBOX" {
		mailboxID = 0 // INBOXのIDは0とする
	} else {
		// メールボックスを取得
		var mailboxData model.Mailbox
		if err := db.Where("name = ? AND user = ?", mailbox, s.username).First(&mailboxData).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, &imap.Error{
					Type: imap.StatusResponseTypeNo,
					Text: "Mailbox does not exist",
				}
			}
			return nil, fmt.Errorf("error finding mailbox: %v", err)
		}
		mailboxID = mailboxData.ID
	}

	// DBとオブジェクトストレージに保存
	buf := &bytes.Buffer{}
	tee := io.TeeReader(r, buf)

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		io.Copy(pw, tee) // S3アップロード用
	}()
	defer pr.Close()

	key, err := objectstorage.MailUploadObject(
		pr,
		conf.ObjectStorage.Region,
		conf.ObjectStorage.Endpoint,
		conf.ObjectStorage.Bucket,
		conf.ObjectStorage.AccessKey,
		conf.ObjectStorage.SecretKey,
	)
	if err != nil {
		log.Fatalf("Error uploading object: %v", err)
	}
	log.Printf("Object uploaded with key: %s", key)
	if err := mailope.SaveMetaData(s.username, buf, key, int64(buf.Len()), db, mailboxID); err != nil {
		log.Fatalf("Error adding mail metadata: %v", err)
	}
	log.Printf("Mail metadata added successfully for key: %s", key)

	return nil, nil
}

func (s *Session) deleteFlagAllDelete() error {
	var mails []model.MessageMetaData
	if err := db.Where("user = ? AND mailbox_id = ? AND JSON_CONTAINS(flags, ?) = 1",
		s.username,
		s.mailboxID,
		fmt.Sprintf("%q", imap.FlagDeleted),
	).Find(&mails).Error; err != nil {
		return fmt.Errorf("error finding messages: %v", err)
	}

	for _, mail := range mails {
		if err := db.Delete(&mail).Error; err != nil {
			return fmt.Errorf("error deleting message metadata: %v", err)
		}

		// オブジェクトストレージからも削除
		if err := objectstorage.DeleteObject(s3Client, conf.ObjectStorage.Bucket, mail.ObjectStorageKey); err != nil {
			log.Printf("Error deleting object from storage: %v", err)
			continue
		}
	}

	return nil
}

// Expunge implements the IMAP EXPUNGE command
func (s *Session) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	log.Println(pp.Sprintf("Expunge called with uids: %v, session: %v", uids, s))

	if uids == nil || len(*uids) == 0 {
		// 現在のメールボックスのメールを削除する
		if err := s.deleteFlagAllDelete(); err != nil {
			return fmt.Errorf("error deleting all messages: %v", err)
		}
		return nil
	}

	for _, uid := range *uids {
		log.Printf("Expunging message with UID: %d", uid)
		var metaData model.MessageMetaData
		tx := db.Begin()
		if err := db.Where("user = ? AND mailbox_id = ? AND id = ?",
			s.username,
			s.mailboxID,
			uid).First(&metaData).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				tx.Rollback()
				log.Printf("Message with UID %d not found for user %s", uid, s.username)
				return fmt.Errorf("message not found: %v", err)
			}
			tx.Rollback()
			return fmt.Errorf("error finding message metadata: %v", err)
		}

		// メッセージを削除
		if err := tx.Delete(&metaData).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("error deleting message metadata: %v", err)
		}

		if err := tx.Commit().Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("error committing transaction: %v", err)
		}

		// オブジェクトストレージからも削除
		if err := objectstorage.DeleteObject(s3Client, conf.ObjectStorage.Bucket, metaData.ObjectStorageKey); err != nil {
			log.Printf("Error deleting object from storage: %v", err)
			continue
		}
	}

	return nil
}

// Search implements the IMAP SEARCH command
func (s *Session) Search(kind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	log.Println(pp.Sprintf("Search called with kind: %v, criteria: %v, options: %v, session: %v", kind, criteria, options, s))

	if kind != imapserver.NumKindUID {
		return nil, &imap.Error{
			Type: imap.StatusResponseTypeBad,
			Text: "Search only supports UID kind",
		}
	}

	if criteria != nil {
		// Collect all matching message IDs from Flag conditions (AND logic)
		var flagResults []uint32
		if len(criteria.Flag) > 0 {
			// Process all flags in criteria.Flag and combine results
			for i, flag := range criteria.Flag {
				result, err := SearchFlag(flag, s.username, s.mailboxID)
				if err != nil {
					return nil, err
				}

				// Convert NumSet to []uint32 for processing
				var currentResults []uint32
				if result.All != nil {
					// We need to convert the NumSet to a slice of uint32
					// Since we can't directly call Numbers(), we'll use a different approach
					// Extract all numbers from the NumSet by iterating through it
					switch v := result.All.(type) {
					case imap.UIDSet:
						for _, uidRange := range v {
							for uid := uidRange.Start; uid <= uidRange.Stop; uid++ {
								currentResults = append(currentResults, uint32(uid))
							}
						}
					case imap.SeqSet:
						for _, seqRange := range v {
							for seq := seqRange.Start; seq <= seqRange.Stop; seq++ {
								currentResults = append(currentResults, uint32(seq))
							}
						}
					}
				}

				// For the first flag, use all results
				if i == 0 {
					flagResults = currentResults
				} else {
					// For subsequent flags, intersect with previous results
					// This implements AND logic between flags
					var intersection []uint32
					for _, prevID := range currentResults {
						for _, currID := range flagResults {
							if prevID == currID {
								intersection = append(intersection, prevID)
								break
							}
						}
					}
					flagResults = intersection
				}
			}
		}

		// Collect all matching message IDs from NotFlag conditions (AND logic)
		var notFlagResults []uint32
		if len(criteria.NotFlag) > 0 {
			// Process all flags in criteria.NotFlag and combine results
			for i, flag := range criteria.NotFlag {
				result, err := SearchFlagNot(flag, s.username, s.mailboxID)
				if err != nil {
					return nil, err
				}

				// Convert NumSet to []uint32 for processing
				var currentResults []uint32
				if result.All != nil {
					// We need to convert the NumSet to a slice of uint32
					switch v := result.All.(type) {
					case imap.UIDSet:
						for _, uidRange := range v {
							for uid := uidRange.Start; uid <= uidRange.Stop; uid++ {
								currentResults = append(currentResults, uint32(uid))
							}
						}
					case imap.SeqSet:
						for _, seqRange := range v {
							for seq := seqRange.Start; seq <= seqRange.Stop; seq++ {
								currentResults = append(currentResults, uint32(seq))
							}
						}
					}
				}

				// For the first flag, use all results
				if i == 0 {
					notFlagResults = currentResults
				} else {
					// For subsequent flags, intersect with previous results
					// This implements AND logic between not flags
					var intersection []uint32
					for _, prevID := range currentResults {
						for _, currID := range notFlagResults {
							if prevID == currID {
								intersection = append(intersection, prevID)
								break
							}
						}
					}
					notFlagResults = intersection
				}
			}
		}

		// Combine flag and not flag results
		// If both flag and not flag conditions exist, we need to exclude not flag results from flag results
		var finalResults []uint32
		if len(flagResults) > 0 && len(notFlagResults) > 0 {
			// Exclude not flag results from flag results
			for _, flagID := range flagResults {
				found := false
				for _, notFlagID := range notFlagResults {
					if flagID == notFlagID {
						found = true
						break
					}
				}
				if !found {
					finalResults = append(finalResults, flagID)
				}
			}
		} else if len(flagResults) > 0 {
			// Only flag conditions exist
			finalResults = flagResults
		} else if len(notFlagResults) > 0 {
			// Only not flag conditions exist
			finalResults = notFlagResults
		} else {
			// No flag conditions, get all messages
			var count int64
			if err := db.Model(&model.MessageMetaData{}).Where("user = ? AND mailbox_id = ?", s.username, s.mailboxID).Count(&count).Error; err != nil {
				return nil, err
			}

			var ids []uint32
			if err := db.Model(&model.MessageMetaData{}).Where("user = ? AND mailbox_id = ?", s.username, s.mailboxID).Pluck("id", &ids).Error; err != nil {
				return nil, err
			}

			finalResults = ids
		}

		// Calculate min, max, and count
		var minID, maxID uint32
		count := len(finalResults)
		if count > 0 {
			minID = finalResults[0]
			maxID = finalResults[0]
			for _, id := range finalResults {
				if id < minID {
					minID = id
				}
				if id > maxID {
					maxID = id
				}
			}
		} else {
			minID = 0
			maxID = 0
		}

		seqnum := imap.SeqSetNum(finalResults...)

		return &imap.SearchData{
			UID:   true,
			All:   seqnum,
			Min:   minID,
			Max:   maxID,
			Count: uint32(count),
		}, nil
	}

	/*
		// DBから削除フラグ付きメールのUID一覧を取得
		var metaData []model.MessageMetaData
		if err := db.Model(&model.MessageMetaData{}).Where("user = ? AND JSON_CONTAINS(flags, ?) = 0", s.username, `"\"\\Delete\""`).Find(&metaData).Error; err != nil {
			return nil, err
		}

		var min, max uint32
		if len(metaData) > 0 {
			min = uint32(metaData[0].ID)
			max = uint32(metaData[len(metaData)-1].ID)
		}

		all := make([]imap.UID, len(metaData))
		for i, meta := range metaData {
			all[i] = imap.UID(meta.ID)
		}

		return &imap.SearchData{
			UID:   false,
			All:   nil,
			Min:   min,
			Max:   max,
			Count: uint32(len(metaData)),
		}, nil
	*/
	return nil, &imap.Error{}
}

// Fetch implements the IMAP FETCH command
func (s *Session) Fetch(w *imapserver.FetchWriter, set imap.NumSet, opts *imap.FetchOptions) error {
	log.Println(pp.Sprintf("Fetch called with set: %v, opts: %v, session: %v", set, opts, s))

	// UIDSetとSeqSetの両方をサポート
	switch v := set.(type) {
	case imap.UIDSet:
		// UIDSetの場合の処理
		if len(v) == 0 {
			return &imap.Error{
				Type: imap.StatusResponseTypeBad,
				Text: "FETCH requires non-empty UIDSet",
			}
		}

		// UIDSetからメッセージを取得
		var messages []model.MessageMetaData
		for _, uidRange := range v {
			var metaData []model.MessageMetaData

			// uidRange.StartとuidRange.Stopで範囲を取得
			// Stopが0の場合は、fetch <Start>:*が指定されているとみなす
			if uidRange.Stop != 0 {
				if err := db.Where(
					"user = ? AND mailbox_id = ? AND id >= ? AND id <= ?",
					s.username,
					s.mailboxID,
					uidRange.Start,
					uidRange.Stop,
				).Find(&metaData).Error; err != nil {
					return &imap.Error{
						Type: imap.StatusResponseTypeNo,
						Text: fmt.Sprintf("Messages with UIDs %d:%d not found", uidRange.Start, uidRange.Stop),
					}
				}
			} else {
				if err := db.Where(
					"user = ? AND mailbox_id = ? AND id >= ?",
					s.username,
					s.mailboxID,
					uidRange.Start,
				).Find(&metaData).Error; err != nil {
					return &imap.Error{
						Type: imap.StatusResponseTypeNo,
						Text: fmt.Sprintf("Message with UID %d not found", uidRange.Start),
					}
				}
			}

			messages = append(messages, metaData...)
		}

		// メッセージを順番に処理
		for i, m := range messages {
			seqNum := uint32(i + 1)
			if err := fetch(m, seqNum, w, opts); err != nil {
				return err
			}
			log.Println(pp.Sprintf("Processing message ID: %d", m.ID))
		}

	case imap.SeqSet:
		// SeqSetの場合の処理
		if len(v) == 0 {
			return &imap.Error{
				Type: imap.StatusResponseTypeBad,
				Text: "FETCH requires non-empty SeqSet",
			}
		}

		// SeqSetからメッセージを取得
		var messages []model.MessageMetaData
		for _, seqRange := range v {
			var m []model.MessageMetaData

			// seqRange.StartとseqRange.Stopで範囲を取得
			// Stopが0の場合は、fetch <Start>:*が指定されているとみなす
			if seqRange.Stop != 0 {
				if err := db.Where(
					"user = ? AND mailbox_id = ?",
					s.username,
					s.mailboxID,
				).Offset(int(seqRange.Start - 1)).
					Limit(int(seqRange.Stop - seqRange.Start + 1)).
					Find(&m).Error; err != nil {
					return &imap.Error{
						Type: imap.StatusResponseTypeNo,
						Text: fmt.Sprintf("Messages with sequence numbers %d:%d not found", seqRange.Start, seqRange.Stop),
					}
				}
			} else {
				if err := db.Where(
					"user = ? AND mailbox_id = ?",
					s.username,
					s.mailboxID,
				).Offset(int(seqRange.Start - 1)).
					Limit(1).
					Find(&m).Error; err != nil {
					return &imap.Error{
						Type: imap.StatusResponseTypeNo,
						Text: fmt.Sprintf("Message with sequence number %d not found", seqRange.Start),
					}
				}
			}

			messages = append(messages, m...)
		}

		// メッセージを順番に処理
		for i, m := range messages {
			seqNum := uint32(i + 1)
			if err := fetch(m, seqNum, w, opts); err != nil {
				return err
			}
			log.Println(pp.Sprintf("Processing message ID: %d", m.ID))
		}

	default:
		return &imap.Error{
			Type: imap.StatusResponseTypeBad,
			Text: "FETCH only supports UIDSet and SeqSet",
		}
	}

	return nil
}

// Store implements the IMAP STORE command
func (s *Session) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	log.Println(pp.Sprintf("Store called with numSet: %v, flags: %v, options: %v, session: %v", numSet, flags, options, s))
	// フラグの更新処理

	if flags == nil || len(flags.Flags) == 0 {
		return &imap.Error{
			Type: imap.StatusResponseTypeBad,
			Text: "STORE requires flags",
		}
	}
	numSetUID, ok := numSet.(imap.UIDSet)
	if !ok {
		return &imap.Error{
			Type: imap.StatusResponseTypeBad,
			Text: "STORE only supports UIDSet",
		}
	}

	for _, uid := range numSetUID {
		var data []model.MessageMetaData
		// rangeで指定
		if err := db.Where("user = ? AND (id >= ? AND id <= ?)", s.username, uid.Start, uid.Stop).
			Find(&data).Error; err != nil {
			return &imap.Error{
				Type: imap.StatusResponseTypeNo,
				Text: fmt.Sprintf("Message with UID %d not found", uid),
			}
		}
		for _, metaData := range data {
			// フラグを更新
			switch flags.Op {
			case imap.StoreFlagsSet:
				metaData.Flags = []string{}
				for _, flag := range flags.Flags {
					metaData.Flags = append(metaData.Flags, string(flag))
					if flag == imap.FlagDeleted {
						if err := setDeleteFlag(&metaData, db); err != nil {
							return &imap.Error{
								Type: imap.StatusResponseTypeNo,
								Text: fmt.Sprintf("Failed to set delete flag: %v", err),
							}
						}
					}
				}
			case imap.StoreFlagsAdd:
				for _, flag := range flags.Flags {
					if !flagsContains(metaData.Flags, string(flag)) {
						metaData.Flags = append(metaData.Flags, string(flag))
					}
					if flag == imap.FlagDeleted {
						if err := setDeleteFlag(&metaData, db); err != nil {
							return &imap.Error{
								Type: imap.StatusResponseTypeNo,
								Text: fmt.Sprintf("Failed to set delete flag: %v", err),
							}
						}
					}
				}
			case imap.StoreFlagsDel:
				for _, flag := range flags.Flags {
					metaData.Flags = flagsRemove(metaData.Flags, string(flag))
				}
			}
			if err := db.Save(&metaData).Error; err != nil {
				return &imap.Error{
					Type: imap.StatusResponseTypeNo,
					Text: fmt.Sprintf("Failed to update flags for UID %d: %v", uid, err),
				}
			}
			log.Println(pp.Sprintf("Updated flags for UID %d: %v", uid, metaData.Flags))
		}
	}

	return nil
}

func setDeleteFlag(metaData *model.MessageMetaData, db *gorm.DB) error {
	trashID, err := getTrashMailboxID(metaData.User, db)
	if err != nil {
		return err
	}
	// すでにTrashにあるメールにDeleteフラグを追加する場合はメールを削除する
	if metaData.MailboxID == trashID {
		if err := db.Delete(&metaData).Error; err != nil {
			return err
		}

		// オブジェクトストレージからも削除
		if err := objectstorage.DeleteObject(s3Client, conf.ObjectStorage.Bucket, metaData.ObjectStorageKey); err != nil {
			log.Printf("Error deleting object from storage: %v", err)
			return err
		}
	}

	metaData.MailboxID = trashID
	return nil
}

func getTrashMailboxID(username string, db *gorm.DB) (uint64, error) {
	var mailbox model.Mailbox
	if err := db.Where("name = ? AND user = ?", "Trash", username).First(&mailbox).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// ない場合は作成して返す
			mailbox = model.Mailbox{
				Name: "Trash",
				User: username,
			}
			if err := db.Create(&mailbox).Error; err != nil {
				return 0, fmt.Errorf("error creating Trash mailbox: %v", err)
			}
			return mailbox.ID, nil
		}
		return 0, fmt.Errorf("error finding Trash mailbox: %v", err)
	}
	return mailbox.ID, nil
}

// Copy implements the IMAP COPY command
func (s *Session) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	log.Println(pp.Sprintf("Copy called with numSet: %v, dest: %s, session: %v", numSet, dest, s))

	if dest == "" {
		return nil, &imap.Error{
			Type: imap.StatusResponseTypeBad,
			Text: "Destination mailbox cannot be empty",
		}
	}

	numSetUID, ok := numSet.(imap.UIDSet)
	if !ok {
		return nil, &imap.Error{
			Type: imap.StatusResponseTypeBad,
			Text: "COPY only supports UIDSet",
		}
	}

	// numSetUIDからメッセージを取得
	var messageMetaData []model.MessageMetaData
	tx := db.Begin()
	for _, uid := range numSetUID {
		var metaData []model.MessageMetaData
		// uid.Startとuid.Stopで範囲を取得
		if uid.Stop != 0 {
			if err := tx.Where(
				"user = ? AND mailbox_id = ? AND(id >= ? AND id <= ?)",
				s.username,
				s.mailboxID,
				uid.Start,
				uid.Stop,
			).Find(&metaData).Error; err != nil {
				tx.Rollback()
				return nil, &imap.Error{
					Type: imap.StatusResponseTypeNo,
					Text: fmt.Sprintf("Message with UID %d not found", uid),
				}
			}
		} else {
			if err := tx.Where(
				"user = ? AND mailbox_id = ? AND id >= ?",
				s.username,
				s.mailboxID,
				uid.Start,
			).Find(&metaData).Error; err != nil {
				tx.Rollback()
				return nil, &imap.Error{
					Type: imap.StatusResponseTypeNo,
					Text: fmt.Sprintf("Message with UID %d not found", uid),
				}
			}
		}
		messageMetaData = append(messageMetaData, metaData...)
	}

	// destからメールボックスを取得
	// 重要な変更：コピー先のメールボックス情報を一時的に保持し、
	// セッションのメールボックス情報は変更しない
	var destMailboxID uint64
	if dest != "INBOX" {
		var mailbox model.Mailbox
		if err := tx.Where("name = ? AND user = ?", dest, s.username).First(&mailbox).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				tx.Rollback()
				return nil, &imap.Error{
					Type: imap.StatusResponseTypeNo,
					Text: "Destination mailbox does not exist",
				}
			}
			tx.Rollback()
			return nil, fmt.Errorf("error finding destination mailbox: %v", err)
		}
		destMailboxID = mailbox.ID
	} else {
		destMailboxID = 0 // INBOXのIDは0とする
	}

	// メッセージのコピー処理
	for _, metaData := range messageMetaData {
		// オブジェクトストレージからメッセージをダウンロード
		obj, err := objectstorage.ObjectDownload(s3Client, conf.ObjectStorage.Bucket, metaData.ObjectStorageKey)
		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("error downloading object: %v", err)
		}
		defer obj.Close()

		// オブジェクトストレージにコピー
		key, err := objectstorage.MailUploadObject(
			obj,
			conf.ObjectStorage.Region,
			conf.ObjectStorage.Endpoint,
			conf.ObjectStorage.Bucket,
			conf.ObjectStorage.AccessKey,
			conf.ObjectStorage.SecretKey,
		)
		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("error uploading object: %v", err)
		}

		log.Printf("Object copied with key: %s", key)

		// メタデータを新しいメールボックスに追加
		newMetaData := model.MessageMetaData{
			User:             s.username,
			Subject:          metaData.Subject,
			From:             metaData.From,
			To:               metaData.To,
			Cc:               metaData.Cc,
			Bcc:              metaData.Bcc,
			MailboxID:        destMailboxID,
			ObjectStorageKey: key,
			Size:             metaData.Size,
			Flags:            metaData.Flags,
			Headers:          metaData.Headers,
			HasAttachments:   metaData.HasAttachments,
			Timestamp:        metaData.Timestamp,
		}
		if err := tx.Create(&newMetaData).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("error creating new message metadata: %v", err)
		}
		log.Printf("New message metadata created with ID: %d", newMetaData.ID)
	}
	if err := tx.Commit().Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("error committing transaction: %v", err)
	}

	return nil, nil
}

// SearchFlag searches for messages with a specific flag
func SearchFlag(flag imap.Flag, username string, mailboxID uint64) (*imap.SearchData, error) {
	var messageMetaData []model.MessageMetaData
	if err := db.Model(&model.MessageMetaData{}).Where(
		"user = ? AND mailbox_id = ? AND JSON_CONTAINS(flags, ?) = 1",
		username,
		mailboxID,
		fmt.Sprintf("%q", flag),
	).Find(&messageMetaData).Error; err != nil {
		return nil, err
	}

	maxID := uint32(0)
	minID := uint32(0)
	idList := make([]uint32, 0, len(messageMetaData))
	for _, meta := range messageMetaData {
		idList = append(idList, uint32(meta.ID))
		if meta.ID > uint64(maxID) {
			maxID = uint32(meta.ID)
		}
		if minID == 0 || meta.ID < uint64(minID) {
			minID = uint32(meta.ID)
		}
	}

	return &imap.SearchData{
		UID:   true,
		All:   imap.SeqSetNum(idList...),
		Min:   minID,
		Max:   maxID,
		Count: uint32(len(messageMetaData)),
	}, nil
}

// SearchFlagNot searches for messages without a specific flag
func SearchFlagNot(flag imap.Flag, username string, mailboxID uint64) (*imap.SearchData, error) {
	var messageMetaData []model.MessageMetaData
	if err := db.Model(&model.MessageMetaData{}).Where(
		"user = ? AND mailbox_id = ? AND JSON_CONTAINS(flags, ?) = 0",
		username,
		mailboxID,
		fmt.Sprintf("%q", flag),
	).Find(&messageMetaData).Error; err != nil {
		return nil, err
	}

	maxID := uint32(0)
	minID := uint32(0)
	idList := make([]uint32, 0, len(messageMetaData))
	for _, meta := range messageMetaData {
		idList = append(idList, uint32(meta.ID))
		if meta.ID > uint64(maxID) {
			maxID = uint32(meta.ID)
		}
		if minID == 0 || meta.ID < uint64(minID) {
			minID = uint32(meta.ID)
		}
	}

	return &imap.SearchData{
		UID:   true,
		All:   imap.SeqSetNum(idList...),
		Min:   minID,
		Max:   maxID,
		Count: uint32(len(messageMetaData)),
	}, nil
}

// flagsContains checks if a flag is in the flags slice
func flagsContains(flags []string, flag string) bool {
	for _, f := range flags {
		if f == flag {
			return true
		}
	}
	return false
}

// flagsRemove removes a flag from the flags slice
func flagsRemove(slice []string, item string) []string {
	for i, v := range slice {
		if v == item {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

// Envelopeを返す関数
func buildEnvelope(m *model.MessageMetaData) *imap.Envelope {
	if m == nil || m.Headers == nil {
		return nil
	}

	var envelope imap.Envelope

	if from, ok := m.Headers["From"]; ok && len(from) > 0 {
		var f []imap.Address
		for _, addr := range from {
			name, mbox, host := mailparser.ParseAddress(addr)
			f = append(f, imap.Address{
				Mailbox: mbox,
				Host:    host,
				Name:    name,
			})
		}
		envelope.From = f
	}
	if to, ok := m.Headers["To"]; ok && len(to) > 0 {
		var t []imap.Address
		for _, addr := range to {
			name, mbox, host := mailparser.ParseAddress(addr)
			t = append(t, imap.Address{
				Mailbox: mbox,
				Host:    host,
				Name:    name,
			})
		}
		envelope.To = t
	}
	if cc, ok := m.Headers["Cc"]; ok && len(cc) > 0 {
		var c []imap.Address
		for _, addr := range cc {
			name, mbox, host := mailparser.ParseAddress(addr)
			c = append(c, imap.Address{
				Mailbox: mbox,
				Host:    host,
				Name:    name,
			})
		}
		envelope.Cc = c
	}
	if bcc, ok := m.Headers["Bcc"]; ok && len(bcc) > 0 {
		var b []imap.Address
		for _, addr := range bcc {
			name, mbox, host := mailparser.ParseAddress(addr)
			b = append(b, imap.Address{
				Mailbox: mbox,
				Host:    host,
				Name:    name,
			})
		}
		envelope.Bcc = b
	}
	if messageID, ok := m.Headers["Message-ID"]; ok && len(messageID) > 0 {
		envelope.MessageID = messageID[0]
	}
	envelope.Date = m.Timestamp

	return &envelope
}

// fetch retrieves and writes message data
func fetch(metaData model.MessageMetaData, seqNum uint32, w *imapserver.FetchWriter, opts *imap.FetchOptions) error {
	msg := w.CreateMessage(seqNum)

	if opts != nil && opts.Envelope {
		msg.WriteEnvelope(buildEnvelope(&metaData))
	}

	if opts != nil && opts.UID {
		msg.WriteUID(imap.UID(metaData.ID))
	}
	if opts != nil && opts.RFC822Size {
		msg.WriteRFC822Size(metaData.Size)
	}
	if opts != nil && opts.Flags {
		var flags []imap.Flag
		for _, flag := range metaData.Flags {
			flags = append(flags, imap.Flag(flag))
		}
		msg.WriteFlags(flags)
	}

	// ヘッダをデータ作成
	if opts != nil && opts.BodySection != nil {
		for _, section := range opts.BodySection {
			switch section.Specifier {
			case imap.PartSpecifierHeader:
				var hb bytes.Buffer
				for k, v := range metaData.Headers {
					if _, err := hb.WriteString(fmt.Sprintf("%s: %s\r\n", k, strings.Join(v, ", "))); err != nil {
						return err
					}
				}
				hb.WriteString("\r\n")

				wr := msg.WriteBodySection(
					&imap.FetchItemBodySection{
						Specifier: imap.PartSpecifierHeader,
					},
					int64(hb.Len()),
				)
				log.Println(pp.Sprintf("Writing header of size: %d", hb.Len()))
				if _, err := wr.Write(hb.Bytes()); err != nil {
					return err
				}
				wr.Close()
			case imap.PartSpecifierNone:
				// メッセージ全体を返す
				obj, err := objectstorage.ObjectDownload(s3Client, conf.ObjectStorage.Bucket, metaData.ObjectStorageKey)
				if err != nil {
					return fmt.Errorf("error downloading object: %v", err)
				}
				defer obj.Close()

				body, err := io.ReadAll(obj)
				if err != nil {
					return fmt.Errorf("error reading message body: %v", err)
				}
				wr := msg.WriteBodySection(section, int64(len(body)))
				log.Println(pp.Sprintf("Writing body of size: %d", len(body)))
				if _, err := wr.Write(body); err != nil {
					return fmt.Errorf("error writing message body: %v", err)
				}
				wr.Close()
			case imap.PartSpecifierText:
				// オブジェクトストレージからメッセージをダウンロード
				obj, err := objectstorage.ObjectDownload(s3Client, conf.ObjectStorage.Bucket, metaData.ObjectStorageKey)
				if err != nil {
					return fmt.Errorf("error downloading object: %v", err)
				}
				defer obj.Close()

				// Body部分だけ返す
				m, err := mail.ReadMessage(obj)
				if err != nil {
					return fmt.Errorf("error reading message: %v", err)
				}
				body, err := io.ReadAll(m.Body)
				if err != nil {
					return fmt.Errorf("error reading message body: %v", err)
				}
				wr := msg.WriteBodySection(section, int64(len(body)))
				log.Println(pp.Sprintf("Writing body of size: %d", len(body)))
				if _, err := wr.Write(body); err != nil {
					return fmt.Errorf("error writing message body: %v", err)
				}
				wr.Close()
			case imap.PartSpecifierMIME:
				// MIMEだけ返す
				obj, err := objectstorage.ObjectDownload(s3Client, conf.ObjectStorage.Bucket, metaData.ObjectStorageKey)
				if err != nil {
					return fmt.Errorf("error downloading object: %v", err)
				}
				defer obj.Close()

				// MIME構造を取得
				m, err := mail.ReadMessage(obj)
				if err != nil {
					return fmt.Errorf("error reading message: %v", err)
				}

				// MIME構造を取得
				var mimeData bytes.Buffer
				if _, err := io.Copy(&mimeData, m.Body); err != nil {
					return fmt.Errorf("error reading MIME data: %v", err)
				}

				wr := msg.WriteBodySection(section, int64(mimeData.Len()))
				log.Println(pp.Sprintf("Writing MIME of size: %d", mimeData.Len()))
				if _, err := wr.Write(mimeData.Bytes()); err != nil {
					return fmt.Errorf("error writing MIME data: %v", err)
				}
				wr.Close()
			default:
				// その他のBODYセクションの処理
				// エラーを回避するために、基本的なヘッダを返す
				var hb bytes.Buffer
				for k, v := range metaData.Headers {
					if _, err := hb.WriteString(fmt.Sprintf("%s: %s\r\n", k, strings.Join(v, ", "))); err != nil {
						return err
					}
				}
				hb.WriteString("\r\n")

				wr := msg.WriteBodySection(
					&imap.FetchItemBodySection{
						Specifier: imap.PartSpecifierHeader,
					},
					int64(hb.Len()),
				)
				log.Println(pp.Sprintf("Writing header of size: %d", hb.Len()))
				if _, err := wr.Write(hb.Bytes()); err != nil {
					return err
				}
				wr.Close()
			}
		}
	}

	if err := msg.Close(); err != nil {
		return err
	}
	return nil
}
