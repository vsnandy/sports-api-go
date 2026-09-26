package players

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type fakeS3 struct {
	getOut *s3.GetObjectOutput
	getErr error
	putIn  *s3.PutObjectInput
}

func (f *fakeS3) GetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return f.getOut, f.getErr
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.putIn = in
	return &s3.PutObjectOutput{}, nil
}

func TestS3StoreMissingKey(t *testing.T) {
	s := S3Store{Client: &fakeS3{getErr: &types.NoSuchKey{}}, Bucket: "b"}
	if _, _, err := s.Get(context.Background(), "players/nfl.json"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestS3StoreRoundTrip(t *testing.T) {
	mod := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	f := &fakeS3{getOut: &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader([]byte("[]"))), LastModified: aws.Time(mod)}}
	s := S3Store{Client: f, Bucket: "b"}
	data, got, err := s.Get(context.Background(), "k")
	if err != nil || string(data) != "[]" || !got.Equal(mod) {
		t.Fatalf("Get = %q, %v, %v", data, got, err)
	}
	if err := s.Put(context.Background(), "k", []byte("[1]")); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(f.putIn.Bucket) != "b" || aws.ToString(f.putIn.Key) != "k" || aws.ToString(f.putIn.ContentType) != "application/json" {
		t.Fatalf("PutObject input = %+v", f.putIn)
	}
}
