package players

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

// S3API is the subset of *s3.Client that S3Store uses.
type S3API interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type S3Store struct {
	Client S3API
	Bucket string
}

func (s S3Store) Get(ctx context.Context, key string) ([]byte, time.Time, error) {
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, time.Time{}, domain.ErrNotFound
		}
		return nil, time.Time{}, err
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	return data, aws.ToTime(out.LastModified), err
}

func (s S3Store) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.Bucket), Key: aws.String(key),
		Body: bytes.NewReader(data), ContentType: aws.String("application/json"),
	})
	return err
}
