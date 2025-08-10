package main

import (
	"bytes"
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/masa23/maild/config"
	"github.com/masa23/maild/model"
	"github.com/masa23/maild/objectstorage"
	"github.com/masa23/maild/savemail"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var (
	conf    *config.Config
	version = "dev"
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

	db, err := gorm.Open(mysql.Open(conf.Database), &gorm.Config{})
	if err != nil {
		log.Fatalf("Error connecting to database: %v", err)
	}
	if err := model.Migrate(db); err != nil {
		log.Fatalf("Error migrating database: %v", err)
	}

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
	if err := savemail.SaveMailMetaData("", buf, key, int64(buf.Len()), db, 0); err != nil {
		log.Fatalf("Error adding mail metadata: %v", err)
	}
	log.Printf("Mail metadata added successfully for key: %s", key)
}
