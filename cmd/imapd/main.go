package main

import (
	"errors"
	"flag"
	"log"
	"net"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/k0kubun/pp/v3"
	"github.com/masa23/maild/config"
	"github.com/masa23/maild/model"
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

func (s *Session) Login(username, password string) error {
	log.Println(pp.Sprintf("Login called with username: %s", username))
	// ToDo: 認証処理
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
	if err := db.Model(&model.MessageMetaData{}).Where("user = ? AND read_flag = false", s.username).Count(&unseenCount).Error; err != nil {
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
			"\\Seen", "\\Answered", "\\Flagged", "\\Deleted", "\\Draft",
		},
		PermanentFlags: []imap.Flag{
			"\\Seen", "\\Deleted", "\\*", // \\* は任意のユーザ定義フラグ許可
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
	db.Model(&model.MessageMetaData{}).Where("user = ? AND read_flag = false", s.username).Count(&unseen)

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
	return errors.New("Unselect not implemented")
}

func (s *Session) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	log.Println(pp.Sprintf("Expunge called with uids: %v", uids))
	return errors.New("Expunge not implemented")
}

func (s *Session) Search(kind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	log.Println(pp.Sprintf("Search called with kind: %v, criteria: %v, options: %v", kind, criteria, options))

	// DBから削除フラグ付きメールのUID一覧を取得
	var metaData []model.MessageMetaData
	if err := db.Model(&model.MessageMetaData{}).Where("user = ? AND deleted_at IS NOT NULL", s.username).Find(&metaData).Error; err != nil {
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
		UID:   true,
		All:   nil,
		Min:   min,
		Max:   max,
		Count: uint32(len(metaData)),
	}, nil
}

func (s *Session) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	log.Println(pp.Sprintf("Fetch called with numSet: %v, options: %v", numSet, options))

	return errors.New("Fetch not implemented")
}

func (s *Session) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	log.Println(pp.Sprintf("Store called with numSet: %v, flags: %v, options: %v", numSet, flags, options))
	return errors.New("Store not implemented")
}

func (s *Session) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	log.Println(pp.Sprintf("Copy called with numSet: %v, dest: %s", numSet, dest))
	return nil, errors.New("Copy not implemented")
}
