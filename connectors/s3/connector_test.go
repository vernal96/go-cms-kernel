package s3

import (
	"context"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awstypes "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/vernal96/go-cms-kernel/filesystem"
)

type mockS3 struct {
	put    *awss3.PutObjectInput
	get    *awss3.GetObjectInput
	delete *awss3.DeleteObjectInput
	lists  []*awss3.ListObjectsV2Input
}

func (*mockS3) HeadBucket(
	context.Context,
	*awss3.HeadBucketInput,
	...func(*awss3.Options),
) (*awss3.HeadBucketOutput, error) {
	return &awss3.HeadBucketOutput{}, nil
}

func (m *mockS3) PutObject(
	_ context.Context,
	input *awss3.PutObjectInput,
	_ ...func(*awss3.Options),
) (*awss3.PutObjectOutput, error) {
	m.put = input
	_, _ = io.ReadAll(input.Body)
	return &awss3.PutObjectOutput{}, nil
}

func (m *mockS3) GetObject(
	_ context.Context,
	input *awss3.GetObjectInput,
	_ ...func(*awss3.Options),
) (*awss3.GetObjectOutput, error) {
	m.get = input
	return &awss3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader("content")),
	}, nil
}

func (m *mockS3) DeleteObject(
	_ context.Context,
	input *awss3.DeleteObjectInput,
	_ ...func(*awss3.Options),
) (*awss3.DeleteObjectOutput, error) {
	m.delete = input
	return &awss3.DeleteObjectOutput{}, nil
}

func (m *mockS3) ListObjectsV2(
	_ context.Context,
	input *awss3.ListObjectsV2Input,
	_ ...func(*awss3.Options),
) (*awss3.ListObjectsV2Output, error) {
	m.lists = append(m.lists, input)
	if len(m.lists) == 1 {
		return &awss3.ListObjectsV2Output{
			Contents:              []awstypes.Object{{Key: aws.String("cms/cache/one")}},
			IsTruncated:           aws.Bool(true),
			NextContinuationToken: aws.String("next"),
		}, nil
	}
	return &awss3.ListObjectsV2Output{
		Contents:    []awstypes.Object{{Key: aws.String("cms/cache/two")}},
		IsTruncated: aws.Bool(false),
	}, nil
}

type mockPresigner struct {
	input   *awss3.GetObjectInput
	expires time.Duration
}

func (m *mockPresigner) PresignGetObject(
	_ context.Context,
	input *awss3.GetObjectInput,
	options ...func(*awss3.PresignOptions),
) (*v4.PresignedHTTPRequest, error) {
	m.input = input
	config := &awss3.PresignOptions{}
	for _, option := range options {
		option(config)
	}
	m.expires = config.Expires
	return &v4.PresignedHTTPRequest{
		URL:    "https://signed.example.test/object",
		Method: "GET",
	}, nil
}

func TestConnectorShapesObjectKeysAndURLs(t *testing.T) {
	client := &mockS3{}
	presigner := &mockPresigner{}
	baseURL, _ := url.Parse("https://cdn.example.test/assets")
	public := newConnector(Config{
		Code:       "public",
		Visibility: filesystem.VisibilityPublic,
		Bucket:     "bucket",
		Prefix:     "cms",
	}, client, presigner, baseURL)

	if err := public.PutNew(
		context.Background(),
		"objects/item",
		strings.NewReader("hello"),
		"text/plain",
	); err != nil {
		t.Fatal(err)
	}
	if client.put == nil ||
		*client.put.Key != "cms/objects/item" ||
		client.put.IfNoneMatch == nil ||
		*client.put.IfNoneMatch != "*" {
		t.Fatalf("put input = %#v", client.put)
	}
	if err := public.Put(
		context.Background(),
		"objects/item",
		strings.NewReader("replacement"),
		"text/plain",
	); err != nil {
		t.Fatal(err)
	}
	if client.put == nil || client.put.IfNoneMatch != nil {
		t.Fatalf("overwrite put input = %#v", client.put)
	}
	rawURL, err := public.URL(
		context.Background(),
		filesystem.Reference{ID: "1", Path: "objects/item"},
	)
	if err != nil || rawURL != "https://cdn.example.test/assets/cms/objects/item" {
		t.Fatalf("public URL = %q, %v", rawURL, err)
	}

	private := newConnector(Config{
		Code:       "private",
		Visibility: filesystem.VisibilityPrivate,
		Bucket:     "bucket",
		Prefix:     "secure",
	}, client, presigner, nil)
	expiresAt := time.Now().Add(30 * time.Minute)
	signed, err := private.TemporaryURL(
		context.Background(),
		filesystem.Reference{ID: "2", Path: "objects/private"},
		expiresAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if signed != "https://signed.example.test/object" ||
		presigner.input == nil ||
		*presigner.input.Key != "secure/objects/private" ||
		presigner.expires <= 0 {
		t.Fatalf(
			"signed URL = %q, input = %#v, expires = %s",
			signed,
			presigner.input,
			presigner.expires,
		)
	}
}

func TestConnectorSpoolsStreamingBodiesAndRemovesTemporaryFile(t *testing.T) {
	temporaryDirectory := t.TempDir()
	t.Setenv("TMPDIR", temporaryDirectory)
	client := &mockS3{}
	connector := newConnector(Config{
		Code: "private", Visibility: filesystem.VisibilityPrivate,
		Bucket: "bucket", Prefix: "cms",
	}, client, &mockPresigner{}, nil)

	if err := connector.PutNew(
		context.Background(),
		"objects/stream",
		io.MultiReader(strings.NewReader("stream")),
		"text/plain",
	); err != nil {
		t.Fatal(err)
	}
	if _, ok := client.put.Body.(io.ReadSeeker); !ok {
		t.Fatalf("streaming body was not made seekable: %T", client.put.Body)
	}
	entries, err := os.ReadDir(temporaryDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary upload files were not removed: %#v", entries)
	}
}

func TestConnectorPrefixScanRetainsContinuationAndReturnsLogicalKeys(t *testing.T) {
	client := &mockS3{}
	connector := newConnector(Config{
		Code: "private", Visibility: filesystem.VisibilityPrivate,
		Bucket: "bucket", Prefix: "cms",
	}, client, &mockPresigner{}, nil)

	scan, err := connector.OpenPrefixScan(context.Background(), "cache")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scan.Close() })
	first, err := scan.Next(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := scan.Next(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Keys) != 1 || first.Keys[0] != "cache/one" || first.Done ||
		len(second.Keys) != 1 || second.Keys[0] != "cache/two" || !second.Done {
		t.Fatalf("prefix pages = %#v, %#v", first, second)
	}
	if len(client.lists) != 2 || aws.ToString(client.lists[0].Prefix) != "cms/cache/" ||
		client.lists[0].ContinuationToken != nil ||
		aws.ToString(client.lists[1].ContinuationToken) != "next" {
		t.Fatalf("list requests = %#v", client.lists)
	}
}
