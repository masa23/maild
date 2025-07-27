package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/mail"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/k0kubun/pp/v3"
	"github.com/masa23/maild/config"
	"github.com/masa23/maild/mailparser"
	"github.com/masa23/maild/model"
	"github.com/masa23/maild/objectstorage"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

var (
	conf     *config.Config
	db       *gorm.DB
	s3Client *s3.S3
	version  = "dev"
)

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

	var err error
	conf, err = config.Load(confPath)
	if err != nil {
		log.Fatal(err)
	}

	s3session := session.Must(session.NewSession(&aws.Config{
		Region:   aws.String(conf.ObjectStorage.Region),
		Endpoint: aws.String(conf.ObjectStorage.Endpoint),
		Credentials: credentials.NewChainCredentials([]credentials.Provider{
			&credentials.StaticProvider{
				Value: credentials.Value{
					AccessKeyID:     conf.ObjectStorage.AccessKey,
					SecretAccessKey: conf.ObjectStorage.SecretKey,
				},
			},
		}),
	}))
	s3Client = s3.New(s3session)

	db, err = gorm.Open(mysql.Open(conf.Database), &gorm.Config{})
	if err != nil {
		log.Fatal(err)
	}

	if err := model.Migrate(db); err != nil {
		log.Fatal(err)
	}

	server := imapserver.New(&imapserver.Options{
		NewSession: func(conn *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &Session{}, &imapserver.GreetingData{}, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {},
			//imap.CapIMAP4rev2: {},
		},
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "0.0.0.0:143")
	if err != nil {
		log.Fatalf("Listen() = %v", err)
	}
	if err := server.Serve(ln); err != nil {
		log.Fatalf("Serve() = %v", err)
	}
}

type Session struct {
	username string
}

func (s *Session) SupportsIMAP4rev2() bool {
	return true
}

func (s *Session) Login(username, password string) error {
	log.Println(pp.Sprintf("Login called with username: %s", username))
	// ToDo: 認証処理
	s.username = "test@mail.masa23.jp"
	return nil
}

func (s *Session) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	log.Println(pp.Sprintf("Poll called with allowExpunge: %v", allowExpunge))
	return nil
}

func (s *Session) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	log.Println(pp.Sprintf("List called with ref: %s, patterns: %v, options: %v", ref, patterns, options))
	list := imap.ListData{
		Attrs:   []imap.MailboxAttr{imap.MailboxAttrHasNoChildren},
		Delim:   '/',
		Mailbox: "INBOX",
	}

	if err := w.WriteList(&list); err != nil {
		return err
	}

	if err := w.WriteList(&imap.ListData{
		Attrs:   []imap.MailboxAttr{imap.MailboxAttrHasNoChildren},
		Delim:   '/',
		Mailbox: "Drafts",
	}); err != nil {
		return err
	}

	if err := w.WriteList(&imap.ListData{
		Attrs:   []imap.MailboxAttr{imap.MailboxAttrHasNoChildren},
		Delim:   '/',
		Mailbox: "Sent",
	}); err != nil {
		return err
	}

	if err := w.WriteList(&imap.ListData{
		Attrs:   []imap.MailboxAttr{imap.MailboxAttrHasNoChildren},
		Delim:   '/',
		Mailbox: "Trash",
	}); err != nil {
		return err
	}

	return nil
}

func (s *Session) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	log.Println(pp.Printf("Select called with mailbox: %s, options: %v", mailbox, options))

	if strings.ToUpper(mailbox) != "INBOX" {
		return nil, &imap.Error{
			Code: imap.ResponseCodeNonExistent,
		}
	}

	// メールボックスのメタデータを取得
	var metaData model.MessageMetaData
	if err := db.Where("user = ?", s.username).Last(&metaData).Error; err != nil {
		return nil, err
	}
	// メールボックスのメッセージ数をカウント
	var count int64
	if err := db.Model(&model.MessageMetaData{}).Where("user = ?", s.username).Count(&count).Error; err != nil {
		return nil, err
	}
	// メールボックスの未読メッセージ数をカウント
	var unseenCount int64
	if err := db.Model(&model.MessageMetaData{}).Where(
		"user = ? AND JSON_CONTAINS(flags, ?) = 0",
		s.username, fmt.Sprintf("%q", imap.FlagDeleted),
	).Count(&unseenCount).Error; err != nil {
		return nil, err
	}

	// 仮データ：実際はストレージなどから取得してください
	numMessages := uint32(count)
	uidNext := imap.UID(metaData.ID) + 1 // UIDはID+1と仮定
	uidValidity := uint32(1)
	highestModSeq := uint64(123456)

	// 基本情報を構築
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

	return data, nil
}

func (s *Session) Create(mailbox string, options *imap.CreateOptions) error {
	log.Println(pp.Sprintf("Create called with mailbox: %s, options: %v", mailbox, options))
	return nil
}

func (s *Session) Delete(mailbox string) error {
	log.Println(pp.Sprintf("Delete called with mailbox: %s", mailbox))
	return errors.New("Delete not implemented")
}

func (s *Session) Rename(mailbox, newName string) error {
	log.Println(pp.Sprintf("Rename called with mailbox: %s, newName: %s", mailbox, newName))
	return errors.New("Rename not implemented")
}

func (s *Session) Subscribe(mailbox string) error {
	log.Println(pp.Sprintf("Subscribe called with mailbox: %s", mailbox))
	return nil
}

func (s *Session) Unsubscribe(mailbox string) error {
	log.Println(pp.Sprintf("Unsubscribe called with mailbox: %s", mailbox))
	return errors.New("Unsubscribe not implemented")
}

func (s *Session) Status(mailbox string, options *imap.StatusOptions) (*imap.StatusData, error) {
	log.Println(pp.Sprintf("Status called with mailbox: %s, options: %v", mailbox, options))
	if strings.ToUpper(mailbox) != "INBOX" {
		return nil, &imap.Error{
			Code: imap.ResponseCodeNonExistent,
		}
	}

	var msgs, unseen int64
	db.Model(&model.MessageMetaData{}).Where("user = ?", s.username).Count(&msgs)
	db.Model(&model.MessageMetaData{}).Where(
		"user = ? AND JSON_CONTAINS(flags, ?) = 0",
		s.username,
		fmt.Sprintf("%q", imap.FlagSeen),
	).Count(&unseen)

	var metaData model.MessageMetaData
	if err := db.Where("user = ?", s.username).Last(&metaData).Error; err != nil {
		return nil, err
	}

	numMessages := uint32(msgs)
	numUnseen := uint32(unseen)

	return &imap.StatusData{
		Mailbox:     mailbox,
		NumMessages: &numMessages,
		NumUnseen:   &numUnseen,
		UIDNext:     imap.UID(metaData.ID + 1), // UIDはID+1と仮定
		UIDValidity: 1,
	}, nil
}

func (s *Session) Append(mailbox string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	log.Println(pp.Sprintf("Append called with mailbox: %s, options: %v", mailbox, options))
	return nil, errors.New("Append not implemented")
}

func (s *Session) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	log.Println(pp.Sprintf("Idle called"))
	return errors.New("Idle not implemented")
}

func (s *Session) Logout() error {
	log.Println(pp.Sprintf("Logout called"))
	return errors.New("Logout not implemented")
}

func (s *Session) Close() error {
	log.Println(pp.Sprintf("Close called"))
	return nil
}

func (s *Session) Unselect() error {
	log.Println(pp.Sprintf("Unselect called"))
	return nil
}

func (s *Session) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	log.Println(pp.Sprintf("Expunge called with uids: %v", uids))
	return nil
}

func (s *Session) Search(kind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	log.Println(pp.Sprintf("Search called with kind: %v, criteria: %v, options: %v", kind, criteria, options))

	if kind != imapserver.NumKindUID {
		return nil, &imap.Error{
			Type: imap.StatusResponseTypeBad,
			Text: "Search only supports UID kind",
		}
	}

	if criteria != nil {
		for _, flag := range criteria.Flag {
			if flag == imap.FlagDeleted {

				// 削除フラグ付きのメールを検索
				// いったん空を返してみる
				return &imap.SearchData{
					UID:   true,
					All:   imap.SeqSetNum(),
					Min:   0,
					Max:   0,
					Count: 0,
				}, nil
			}
			if flag == imap.FlagAnswered {
				// 返信済みのメールを検索
				// いったん空を返してみる
				return &imap.SearchData{
					UID:   true,
					All:   imap.SeqSetNum(),
					Min:   0,
					Max:   0,
					Count: 0,
				}, nil
			}
			if flag == imap.FlagDraft {
				// 下書きのメールを検索
				// いったん空を返してみる
				return &imap.SearchData{
					UID:   true,
					All:   imap.SeqSetNum(),
					Min:   0,
					Max:   0,
					Count: 0,
				}, nil
			}
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

// Envelopeを返す関数
func buildEnvelope(m *model.MessageMetaData) *imap.Envelope {
	name, mbox, host := mailparser.ParseAddress(m.FromRaw)
	from := []imap.Address{
		{
			Mailbox: mbox,
			Host:    host,
			Name:    name,
		},
	}
	name, mbox, host = mailparser.ParseAddress(m.ToRaw)
	to := []imap.Address{
		{
			Mailbox: mbox,
			Host:    host,
			Name:    name,
		},
	}
	name, mbox, host = mailparser.ParseAddress(m.CcRaw)
	cc := []imap.Address{
		{
			Mailbox: mbox,
			Host:    host,
			Name:    name,
		},
	}
	name, mbox, host = mailparser.ParseAddress(m.BccRaw)
	bcc := []imap.Address{
		{
			Mailbox: mbox,
			Host:    host,
			Name:    name,
		},
	}

	return &imap.Envelope{
		Subject:   m.Subject,
		From:      from,
		To:        to,
		Cc:        cc,
		Bcc:       bcc,
		MessageID: m.MessageID,
		Date:      m.Timestamp,
	}
}

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
			case imap.PartSpecifierNone, imap.PartSpecifierText:
				// オブジェクトストレージからメッセージをダウンロード
				obj, err := objectstorage.ObjectDownload(s3Client, conf.ObjectStorage.Bucket, metaData.ObjectStorageKey)
				if err != nil {
					return fmt.Errorf("error downloading object: %v", err)
				}
				defer obj.Close()

				if section.Specifier == imap.PartSpecifierText {
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

				} else {
					// メッセージをすべて返す
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
				}
			case imap.PartSpecifierMIME:
				// MIMEだけ返すらしい
			}
		}
	}

	if err := msg.Close(); err != nil {
		return err
	}
	return nil
}

func (s *Session) Fetch(w *imapserver.FetchWriter, set imap.NumSet, opts *imap.FetchOptions) error {
	log.Println(pp.Sprintf("Fetch called with set: %v, opts: %v", set, opts))
	uidSet, ok := set.(imap.UIDSet)
	log.Println(pp.Sprintf("fetch uid or seq: %v %b", uidSet, ok))

	if !ok {
		// UIDではない場合、SeqSetとして処理
		seq := set.(imap.SeqSet)
		messages := []struct {
			model  model.MessageMetaData
			number uint32
		}{}
		for _, seqnum := range seq {
			// seqnumは1:100の場合は、行頭から100件、200:300の場合は、201から300件を取得する
			// stringからstartとendを取得
			start := seqnum.Start
			stop := seqnum.Stop
			var m []model.MessageMetaData
			if err := db.
				Where("user = ?", s.username).
				Offset(int(start - 1)).
				Limit(int(stop - start + 1)).
				Find(&m).Error; err != nil {
				return err
			}
			for i, meta := range m {
				messages = append(messages, struct {
					model  model.MessageMetaData
					number uint32
				}{
					model:  meta,
					number: seqnum.Start + uint32(i),
				})
			}
		}

		// seqnumは1:100の場合は、行頭から100件、200:300の場合は、201から300件を取得する
		// stringからstartとendを取得
		for _, meta := range messages {
			metaData := meta.model
			if err := fetch(metaData, meta.number, w, opts); err != nil {
				return err
			}
			log.Println(pp.Sprintf("Processing message ID: %d", metaData.ID))
		}

		return nil
	} else {
		// UIDSetの場合の処理
		if len(uidSet) == 0 {
			return &imap.Error{
				Type: imap.StatusResponseTypeBad,
				Text: "FETCH requires non-empty UIDSet",
			}
		}

		var messages []model.MessageMetaData
		for _, uid := range uidSet {
			var metaData []model.MessageMetaData
			log.Println(pp.Sprintf("Fetching message with UID: %d", uid))
			// uid.Startとuid.Stopで範囲を取得
			if err := db.Where("user = ? AND (id >= ? AND id <= ?)", s.username, uid.Start, uid.Stop).
				Find(&metaData).Error; err != nil {
				return &imap.Error{
					Type: imap.StatusResponseTypeNo,
					Text: fmt.Sprintf("Message with UID %d not found", uid),
				}
			}
			messages = append(messages, metaData...)
		}

		// messagesが1件の場合は、fetchを呼び出す
		if len(messages) == 1 {
			if err := fetch(messages[0], 1, w, opts); err != nil {
				return err
			}
		}

		// 複数件の場合は、ループで処理
		for i, m := range messages {
			uid := imap.UID(m.ID)
			if !uidSet.Contains(uid) {
				continue
			}

			if err := fetch(m, uint32(i+1), w, opts); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Session) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	log.Println(pp.Sprintf("Store called with numSet: %v, flags: %v, options: %v", numSet, flags, options))
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

func flagsContains(flags []string, flag string) bool {
	for _, f := range flags {
		if f == flag {
			return true
		}
	}
	return false
}

func flagsRemove(slice []string, item string) []string {
	for i, v := range slice {
		if v == item {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

func (s *Session) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	log.Println(pp.Sprintf("Copy called with numSet: %v, dest: %s", numSet, dest))
	return nil, errors.New("Copy not implemented")
}
