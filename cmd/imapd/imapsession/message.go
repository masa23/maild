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
	"github.com/masa23/maild/mailparser"
	"github.com/masa23/maild/model"
	"github.com/masa23/maild/objectstorage"
	"github.com/masa23/maild/savemail"
	"gorm.io/gorm"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

// Append implementsmthe plements thecommandND command
func (s *Session) Append(mailbox string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	log.Println(pp.Sprintf("Append calledawith lled with mailbox: %s, o", mailbox, options))

	if mailbox == "" {
		return nil, &imap.Error{
			Type: imap.StatusResponseTypeBad,
			Text: "Mailbox name cannot be empty",
		}
	}

	if mailbox == "INBOX" {
		s.mailboxID = 0 // INBOXのIDは0とする
		s.mailbox = "INBOX"
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
		s.mailboxID = mailboxData.ID
		s.mailbox = mailbox
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
	if err := savemail.SaveMailMetaData(s.username, buf, key, int64(buf.Len()), db, s.mailboxID); err != nil {
		log.Fatalf("Error adding mail metadata: %v", err)
	}
	log.Printf("Mail metadata added successfully for key: %s", key)

	return nil, nil
}

// Expunge implements the IMAP EXPUNGE command
func (s *Session) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	log.Println(pp.Sprintf("Expunge called with uids: %v", uids))

	if uids == nil || len(*uids) == 0 {
		return &imap.Error{
			Type: imap.StatusResponseTypeBad,
			Text: "UIDs cannot be nil",
		}
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
				continue
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
	log.Println(pp.Sprintf("Search called with kind: %v, criteria: %v, options: %v", kind, criteria, options))

	if kind != imapserver.NumKindUID {
		return nil, &imap.Error{
			Type: imap.StatusResponseTypeBad,
			Text: "Search only supports UID kind",
		}
	}

	if criteria != nil {
		// ToDo: 検索条件に応じて処理を分岐
		// 本当は複数の条件をサポートするが、現状は単一のフラグ検索のみ
		for _, flag := range criteria.Flag {
			return SearchFlag(flag, s.username, s.mailboxID)
		}

		for _, flag := range criteria.NotFlag {
			return SearchFlagNot(flag, s.username, s.mailboxID)
		}

		// 件数
		var count int64
		if err := db.Model(&model.MessageMetaData{}).Where("user = ?", s.username).Count(&count).Error; err != nil {
			return nil, err
		}
		// 一番小さいIDと最大のIDを取得
		var minID, maxID uint32
		if count > 0 {
			var metaData model.MessageMetaData
			if err := db.Where("user = ?", s.username).Order("id ASC").First(&metaData).Error; err != nil {
				return nil, err
			}
			minID = uint32(metaData.ID)
			if err := db.Where("user = ?", s.username).Order("id DESC").First(&metaData).Error; err != nil {
				return nil, err
			}
			maxID = uint32(metaData.ID)
		} else {
			minID = 0
			maxID = 0
		}

		var ids []uint32
		if err := db.Model(&model.MessageMetaData{}).Where("user = ?", s.username).Pluck("id", &ids).Error; err != nil {
			return nil, err
		}

		seqnum := imap.SeqSetNum(ids...)

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
	log.Println(pp.Sprintf("Fetch called with set: %v, opts: %v", set, opts))

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
	log.Println(pp.Sprintf("Store called with numSet: %v, flags: %v, options: %v", numSet, flags, options))
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
				}
			case imap.StoreFlagsAdd:
				for _, flag := range flags.Flags {
					if !flagsContains(metaData.Flags, string(flag)) {
						metaData.Flags = append(metaData.Flags, string(flag))
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

// Copy implements the IMAP COPY command
func (s *Session) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	log.Println(pp.Sprintf("Copy called with numSet: %v, dest: %s", numSet, dest))

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
		s.mailboxID = mailbox.ID
		s.mailbox = dest
	} else {
		s.mailboxID = 0 // INBOXのIDは0とする
		s.mailbox = "INBOX"
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
			MailboxID:        s.mailboxID,
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

// IsDraftsEmpty checks if the Drafts mailbox is empty for the current user
func (s *Session) IsDraftsEmpty() (bool, error) {
	return IsDraftsEmpty(s.username, db)
}

// GetDraftsStatus returns detailed status of the Drafts mailbox
func (s *Session) GetDraftsStatus() (*DraftsStatus, error) {
	return CheckDraftsStatus(s.username, db)
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

// IsDraftsEmpty checks if the Drafts mailbox is empty for a given user
func IsDraftsEmpty(username string, db *gorm.DB) (bool, error) {
	// Find the Drafts mailbox ID
	var draftsMailbox model.Mailbox
	if err := db.Where("name = ? AND user = ?", "Drafts", username).First(&draftsMailbox).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// If Drafts mailbox doesn't exist, it's effectively empty
			return true, nil
		}
		return false, fmt.Errorf("error finding Drafts mailbox: %v", err)
	}

	// Count messages in the Drafts mailbox
	var count int64
	if err := db.Model(&model.MessageMetaData{}).Where(
		"user = ? AND mailbox_id = ?",
		username,
		draftsMailbox.ID,
	).Count(&count).Error; err != nil {
		return false, fmt.Errorf("error counting messages in Drafts: %v", err)
	}

	// Return true if no messages found (empty)
	return count == 0, nil
}

// CheckDraftsStatus provides detailed information about the Drafts mailbox status
func CheckDraftsStatus(username string, db *gorm.DB) (*DraftsStatus, error) {
	status := &DraftsStatus{}

	// Find the Drafts mailbox
	var draftsMailbox model.Mailbox
	if err := db.Where("name = ? AND user = ?", "Drafts", username).First(&draftsMailbox).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// If Drafts mailbox doesn't exist, return empty status
			status.Exists = false
			status.IsEmpty = true
			status.MessageCount = 0
			status.UnseenCount = 0
			return status, nil
		}
		return nil, fmt.Errorf("error finding Drafts mailbox: %v", err)
	}

	status.Exists = true
	status.MailboxID = draftsMailbox.ID

	// Get message count
	var count int64
	if err := db.Model(&model.MessageMetaData{}).Where(
		"user = ? AND mailbox_id = ?",
		username,
		draftsMailbox.ID,
	).Count(&count).Error; err != nil {
		return nil, fmt.Errorf("error counting messages in Drafts: %v", err)
	}
	status.MessageCount = int(count)

	// Get unseen message count
	var unseenCount int64
	if err := db.Model(&model.MessageMetaData{}).Where(
		"user = ? AND mailbox_id = ? AND JSON_CONTAINS(flags, ?) = 0",
		username,
		draftsMailbox.ID,
		fmt.Sprintf("%q", "\\Seen"),
	).Count(&unseenCount).Error; err != nil {
		return nil, fmt.Errorf("error counting unseen messages in Drafts: %v", err)
	}
	status.UnseenCount = int(unseenCount)

	// Check if empty
	status.IsEmpty = count == 0

	// Get UIDNext (next available UID)
	var metaData model.MessageMetaData
	if err := db.Where(
		"user = ? AND mailbox_id = ?",
		username,
		draftsMailbox.ID,
	).Last(&metaData).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("error finding last message metadata: %v", err)
	}
	status.UIDNext = metaData.ID + 1

	return status, nil
}

// DraftsStatus holds information about the Drafts mailbox status
type DraftsStatus struct {
	Exists       bool
	MailboxID    uint64
	MessageCount int
	UnseenCount  int
	IsEmpty      bool
	UIDNext      uint64
}
