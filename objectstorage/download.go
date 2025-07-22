package objectstorage

import (
	"io"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/valyala/gozstd"
)

func ObjectDownload(s3Client *s3.S3, bucket, key string) (io.ReadCloser, error) {
	if !strings.HasSuffix(key, ".zstd") {
		key = key + ".eml"
	}
	resp, err := s3Client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}

	if strings.HasSuffix(key, ".zstd") {
		zstd := gozstd.NewReader(resp.Body)
		return struct {
			io.Reader
			io.Closer
		}{
			Reader: zstd,
			Closer: resp.Body,
		}, nil

	}
	return resp.Body, nil
}
