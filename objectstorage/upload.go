package objectstorage

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"github.com/valyala/gozstd"
)

// Check if object exists
func ObjectExists(s3Client *s3.S3, bucket, key string) (bool, error) {
	resp, err := s3Client.HeadObject(&s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case s3.ErrCodeNoSuchKey:
				return false, nil
			default:
				return false, err
			}
		}
	}
	return resp != nil, nil
}

// Upload object with zstd compression
// ToDo: Memory efficiency could be improved as buf is used
func UploadObjectWithZstd(s3Client *s3.S3, bucket, key string, reader io.Reader) error {
	// Compress with zstd
	var buf bytes.Buffer
	zw := gozstd.NewWriter(&buf)
	if _, err := io.Copy(zw, reader); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	_, err := s3Client.PutObject(&s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key + ".zstd"),
		Body:        bytes.NewReader(buf.Bytes()),
		ContentType: aws.String("application/zstd"),
	})
	if err != nil {
		return err
	}
	return nil
}

// Upload object
// ToDo: Memory efficiency could be improved as buf is used
func UploadObject(s3Client *s3.S3, bucket, key string, reader io.Reader) error {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, reader); err != nil {
		return err
	}
	_, err := s3Client.PutObject(&s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key + ".eml"),
		Body:        bytes.NewReader(buf.Bytes()),
		ContentType: aws.String("application/octet-stream"),
	})
	if err != nil {
		return err
	}
	return nil
}

// Generate object key with current timestamp
// YYYY/MM/DD/HH/mm/ss/UUID
func GenerateObjectKey() string {
	now := time.Now()
	return fmt.Sprintf("%04d/%02d/%02d/%02d/%02d/%02d/%s",
		now.Year(), now.Month(), now.Day(),
		now.Hour(), now.Minute(), now.Second(),
		uuid.New().String())
}

func MailUploadObject(r io.Reader, region, endpoint, bucket, accessKey, secretKey string) (string, error) {
	s3session := session.Must(session.NewSession(&aws.Config{
		Region:   aws.String(region),
		Endpoint: aws.String(endpoint),
		Credentials: credentials.NewChainCredentials([]credentials.Provider{
			&credentials.StaticProvider{
				Value: credentials.Value{
					AccessKeyID:     accessKey,
					SecretAccessKey: secretKey,
				},
			},
		}),
	}))
	s3Client := s3.New(s3session)

	var key string
	for {
		key = GenerateObjectKey()
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

	/* Development: zstd compression is disabled
	if err := objectstorage.UploadObjectWithZstd(s3Client, conf.ObjectStorage.Bucket, key, pr); err != nil {
		log.Fatalf("Error uploading object to S3: %v", err)
	}
	*/
	if err := UploadObject(s3Client, bucket, key, r); err != nil {
		return "", fmt.Errorf("error uploading object to S3: %v", err)
	}

	return key, nil
}
