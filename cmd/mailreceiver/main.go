package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/masa23/maild/config"
	"github.com/masa23/maild/model"
	"github.com/masa23/maild/objectstorage"
	"golang.org/x/text/encoding/japanese"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var (
	conf    *config.Config
	version = "dev"
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

func decodeHeader(header string) (string, error) {
	dec := new(mime.WordDecoder)
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		switch strings.ToLower(charset) {
		case "iso-2022-jp":
			return japanese.ISO2022JP.NewDecoder().Reader(input), nil
		default:
			return input, nil
		}
	}
	decoded, err := dec.DecodeHeader(header)
	if err != nil {
		return "", err
	}
	return decoded, nil
}

func addMailMetaData(r io.Reader, objKey string, size int64) error {
	msg, err := mail.ReadMessage(r)
	if err != nil {
		log.Fatalf("Error reading message: %v", err)
	}

	db, err := gorm.Open(mysql.Open(conf.Database), &gorm.Config{})
	if err != nil {
		log.Fatalf("Error connecting to database: %v", err)
	}

	if err := model.Migrate(db); err != nil {
		log.Fatalf("Error migrating database: %v", err)
	}

	// Date To time.Time conversion
	timestamp, _ := msg.Header.Date()

	hasAttachments, err := hasAttachments(msg)
	if err != nil {
		return fmt.Errorf("error checking for attachments: %v", err)
	}

	// subject decoding
	subject := msg.Header.Get("Subject")
	decodedSubject, err := decodeHeader(subject)
	if err != nil {
		log.Printf("Error decoding subject=%s err=%v", subject, err)
		decodedSubject = subject // Fallback to original subject if decoding fails
	}
	decodedFrom, err := decodeHeader(msg.Header.Get("From"))
	if err != nil {
		log.Printf("Error decoding From header: %v", err)
		decodedFrom = msg.Header.Get("From") // Fallback to original From if decoding fails
	}
	decodedTo, err := decodeHeader(msg.Header.Get("To"))
	if err != nil {
		log.Printf("Error decoding To header: %v", err)
		decodedTo = msg.Header.Get("To") // Fallback to original To if decoding fails
	}
	decodedCc, err := decodeHeader(msg.Header.Get("Cc"))
	if err != nil {
		log.Printf("Error decoding Cc header: %v", err)
		decodedCc = msg.Header.Get("Cc") // Fallback to original Cc if decoding fails
	}
	decodedBcc, err := decodeHeader(msg.Header.Get("Bcc"))
	if err != nil {
		log.Printf("Error decoding Bcc header: %v", err)
		decodedBcc = msg.Header.Get("Bcc") // Fallback to original Bcc if decoding fails
	}

	metaData := model.MessageMetaData{
		User:             msg.Header.Get("Delivered-To"),
		Subject:          decodedSubject,
		FromRaw:          msg.Header.Get("From"),
		ToRaw:            msg.Header.Get("To"),
		CcRaw:            msg.Header.Get("Cc"),
		BccRaw:           msg.Header.Get("Bcc"),
		From:             decodedFrom,
		To:               decodedTo,
		Cc:               decodedCc,
		Bcc:              decodedBcc,
		Size:             size,
		MessageID:        msg.Header.Get("Message-ID"),
		References:       msg.Header.Get("References"),
		Timestamp:        timestamp,
		HasAttachments:   hasAttachments,
		Flags:            nil,
		ObjectStorageKey: objKey,
	}

	if err := db.Create(&metaData).Error; err != nil {
		return fmt.Errorf("error saving metadata to database: %v", err)
	}
	log.Printf("Metadata saved with ID: %d", metaData.ID)

	return nil
}

func uploadObject(r io.Reader) (string, error) {
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
	s3Client := s3.New(s3session)

	var key string
	for {
		key = objectstorage.GenerateObjectKey()
		/*
			found, err := objectstorage.ObjectExists(s3Client, conf.ObjectStorage.Bucket, key)
			if found {
				continue
			}
			if err != nil {
				log.Fatalf("Error checking if object exists: %v", err)
			}*/
		break
	}

	/* 開発中なのでzstd圧縮はしない
	if err := objectstorage.UploadObjectWithZstd(s3Client, conf.ObjectStorage.Bucket, key, pr); err != nil {
		log.Fatalf("Error uploading object to S3: %v", err)
	}
	*/
	if err := objectstorage.UploadObject(s3Client, conf.ObjectStorage.Bucket, key, r); err != nil {
		return "", fmt.Errorf("error uploading object to S3: %v", err)
	}

	return key, nil
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

	// logfile
	logFd, err := os.OpenFile(conf.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Error opening log file: %v", err)
	}
	defer logFd.Close()
	log.SetOutput(logFd)

	log.Printf("start mail upload process pid=%d", os.Getpid())

	buf := &bytes.Buffer{}
	tee := io.TeeReader(os.Stdin, buf)

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		io.Copy(pw, tee) // S3アップロード用
	}()
	defer pr.Close()

	key, err := uploadObject(pr)
	if err != nil {
		log.Fatalf("Error uploading object: %v", err)
	}
	log.Printf("Object uploaded with key: %s", key)
	if err := addMailMetaData(buf, key, int64(buf.Len())); err != nil {
		log.Fatalf("Error adding mail metadata: %v", err)
	}
	log.Printf("Mail metadata added successfully for key: %s", key)
}
