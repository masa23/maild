package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
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
			//imap.FlagSeen, imap.FlagDeleted, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDraft,
		},
		PermanentFlags: []imap.Flag{
			//imap.FlagSeen, imap.FlagDeleted,
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

	if criteria != nil {
		for _, flag := range criteria.Flag {
			if flag == imap.FlagDeleted {
				// 削除フラグ付きのメールを検索
				// いったん空を返してみる
				return nil, &imap.Error{
					Code: imap.ResponseCodeNonExistent,
				}
			}
			if flag == imap.FlagAnswered {
				// 返信済みのメールを検索
				// いったん空を返してみる
				return nil, &imap.Error{
					Code: imap.ResponseCodeNonExistent,
				}
			}
			if flag == imap.FlagDraft {
				// 下書きのメールを検索
				// いったん空を返してみる
				return nil, &imap.Error{
					Code: imap.ResponseCodeNonExistent,
				}
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

		seqnum := imap.SeqSetNum(100)

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

func (s *Session) Fetch(w *imapserver.FetchWriter, set imap.NumSet, opts *imap.FetchOptions) error {
	log.Println(pp.Sprintf("Fetch called with set: %v, opts: %v", set, opts))
	uidSet, ok := set.(imap.UIDSet)
	if !ok {
		/*
			return &imap.Error{
				Type: imap.StatusResponseTypeBad,
				Text: "FETCH only supports UIDSet",
			}
		*/

		// 最新のメッセージを取得するために、UIDSetを仮に作成
		var metaData model.MessageMetaData
		if err := db.Where("user = ?", s.username).Last(&metaData).Error; err != nil {
			return err
		}
		name, mbox, host := mailparser.ParseAddress(metaData.FromRaw)
		from := []imap.Address{
			{
				Mailbox: mbox,
				Host:    host,
				Name:    name,
			},
		}
		name, mbox, host = mailparser.ParseAddress(metaData.ToRaw)
		to := []imap.Address{
			{
				Mailbox: mbox,
				Host:    host,
				Name:    name,
			},
		}
		name, mbox, host = mailparser.ParseAddress(metaData.CcRaw)
		cc := []imap.Address{
			{
				Mailbox: mbox,
				Host:    host,
				Name:    name,
			},
		}
		name, mbox, host = mailparser.ParseAddress(metaData.BccRaw)
		bcc := []imap.Address{
			{
				Mailbox: mbox,
				Host:    host,
				Name:    name,
			},
		}

		msg := w.CreateMessage(uint32(metaData.ID))
		msg.WriteEnvelope(&imap.Envelope{
			Subject:   metaData.Subject,
			From:      from,
			To:        to,
			Cc:        cc,
			Bcc:       bcc,
			MessageID: metaData.MessageID,
			Date:      metaData.Timestamp,
		})
		msg.WriteUID(imap.UID(metaData.ID))
		msg.WriteRFC822Size(metaData.Size)
		wr := msg.WriteBodySection(
			&imap.FetchItemBodySection{
				Specifier: imap.PartSpecifierText,
			},
			metaData.Size,
		)

		body, err := objectstorage.ObjectDownload(s3Client, conf.ObjectStorage.Bucket, metaData.ObjectStorageKey)
		if err != nil {
			return err
		}
		defer body.Close()
		if _, err := io.Copy(wr, body); err != nil {
			return err
		}
		if err := wr.Close(); err != nil {
			return err
		}

		if err := msg.Close(); err != nil {
			return err
		}
		return nil
	}

	var messages []model.MessageMetaData
	if err := db.Where("user = ?", s.username).Find(&messages).Error; err != nil {
		return err
	}

	for _, m := range messages {
		uid := imap.UID(m.ID)
		if !uidSet.Contains(uid) {
			continue
		}

		seqNum := uint32(m.ID) // 仮：UID = SeqNum
		msg := w.CreateMessage(seqNum)

		// UIDの明示出力は不要。go-imapが内部で付加してくれる
		var flags []imap.Flag
		for _, flag := range m.Flags {
			flags = append(flags, imap.Flag(flag))
		}
		msg.WriteFlags(flags)

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

		msg.WriteEnvelope(&imap.Envelope{
			Subject:   m.Subject,
			From:      from,
			To:        to,
			Cc:        cc,
			Bcc:       bcc,
			MessageID: m.MessageID,
			Date:      m.Timestamp,
		})
		body, err := objectstorage.ObjectDownload(s3Client, conf.ObjectStorage.Bucket, m.ObjectStorageKey)
		if err != nil {
			return err
		}
		defer body.Close()

		msg.WriteRFC822Size(m.Size)

		wr := msg.WriteBodySection(
			&imap.FetchItemBodySection{
				Specifier: imap.PartSpecifierText,
			},
			m.Size,
		)
		if _, err := io.Copy(wr, body); err != nil {
			return err
		}
		wr.Close()

		if err := msg.Close(); err != nil {
			return err
		}
	}

	return nil
}

func (s *Session) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	log.Println(pp.Sprintf("Store called with numSet: %v, flags: %v, options: %v", numSet, flags, options))
	return errors.New("Store not implemented")
}

func (s *Session) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	log.Println(pp.Sprintf("Copy called with numSet: %v, dest: %s", numSet, dest))
	return nil, errors.New("Copy not implemented")
}
