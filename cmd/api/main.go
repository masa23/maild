package main

import (
	"flag"
	"io"
	"log"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/masa23/maild/config"
	"github.com/masa23/maild/model"
	"github.com/masa23/maild/objectstorage"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var (
	conf     *config.Config
	db       *gorm.DB
	s3Client *s3.S3
	version  = "dev"
)

func getList(c echo.Context) error {
	var messages []model.MessageMetaData

	// CreatedAtで降順にソート
	if err := db.Order("created_at DESC").Find(&messages).Error; err != nil {
		return c.JSON(500, map[string]string{"error": "Failed to fetch messages"})
	}

	return c.JSON(200, messages)
}

type Message struct {
	Body string `json:"body"`
	model.MessageMetaData
}

func getMessage(c echo.Context) error {
	id := c.Param("id")
	var message model.MessageMetaData

	if err := db.First(&message, id).Error; err != nil {
		return c.JSON(404, map[string]string{"error": "Message not found"})
	}

	msg, err := objectstorage.ObjectDownload(s3Client, conf.ObjectStorage.Bucket, message.ObjectStorageKey)
	if err != nil {
		c.Logger().Error("Failed to download message:", err)
		return c.JSON(500, map[string]string{"error": "Failed to download message"})
	}
	defer msg.Close()

	body, err := io.ReadAll(msg)
	if err != nil {
		return c.JSON(500, map[string]string{"error": "Failed to read message body"})
	}

	return c.JSON(200, Message{
		Body:            string(body),
		MessageMetaData: message,
	})
}

func main() {
	var confPath string
	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "Show version")
	flag.StringVar(&confPath, "config", "config.yaml", "Path to config file")
	flag.Parse()

	if showVersion {
		log.Printf("Version: %s", version)
		return
	}

	var err error
	conf, err = config.Load(confPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
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

	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins:     []string{"*"},
		AllowHeaders:     []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAuthorization},
		AllowCredentials: true,
	}))

	db, err = gorm.Open(mysql.Open(conf.Database), &gorm.Config{})
	if err != nil {
		e.Logger.Fatal("DB connection failed:", err)
	}
	if err := model.Migrate(db); err != nil {
		e.Logger.Fatal("Migration failed:", err)
	}

	// ルーティング
	e.GET("/api/list", getList)
	e.GET("/api/message/:id", getMessage)
	/*
		e.POST("/auth/login", loginHandler)
		e.GET("/auth/refresh", refreshHandler)
		e.POST("/auth/logout", logoutHandler)

		api := e.Group("/api")
		api.Use(echojwt.WithConfig(echojwt.Config{
			NewClaimsFunc: auth.NewJWTClaims,
			SigningKey:    []byte(conf.AccessToken.JWTSecret),
		}))

		api.GET("/profile", profileHandler)
	*/
	e.Logger.Fatal(e.Start(":8080"))
}
