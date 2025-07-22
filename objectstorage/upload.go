package objectstorage

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"github.com/valyala/gozstd"
)

// オブジェクトが存在するか
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

// オブジェクトをアップロードする zstd圧縮する
// ToDo: bufを使っているのでメモリ効率が悪い
func UploadObjectWithZstd(s3Client *s3.S3, bucket, key string, reader io.Reader) error {
	// zstd圧縮する
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

// オブジェクトをアップロードする
// ToDo: bufを使っているのでメモリ効率が悪い
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

// 現在の時刻でオブジェクトのキーを生成する
// YYYY/MM/DD/HH/mm/ss/UUID
func GenerateObjectKey() string {
	now := time.Now()
	return fmt.Sprintf("%04d/%02d/%02d/%02d/%02d/%02d/%s",
		now.Year(), now.Month(), now.Day(),
		now.Hour(), now.Minute(), now.Second(),
		uuid.New().String())
}
