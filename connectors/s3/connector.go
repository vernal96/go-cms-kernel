package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/vernal96/go-cms-kernel/filesystem"
)

type Config struct {
	Code            filesystem.Code
	Visibility      filesystem.Visibility
	Region          string
	Bucket          string
	Prefix          string
	Endpoint        string
	UsePathStyle    bool
	PublicBaseURL   string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

type Factory struct {
	Config Config
}

func (f Factory) Code() filesystem.Code {
	return f.Config.Code
}

func (f Factory) Open(ctx context.Context) (filesystem.Disk, error) {
	return New(ctx, f.Config)
}

type objectAPI interface {
	HeadBucket(
		context.Context,
		*awss3.HeadBucketInput,
		...func(*awss3.Options),
	) (*awss3.HeadBucketOutput, error)
	PutObject(
		context.Context,
		*awss3.PutObjectInput,
		...func(*awss3.Options),
	) (*awss3.PutObjectOutput, error)
	GetObject(
		context.Context,
		*awss3.GetObjectInput,
		...func(*awss3.Options),
	) (*awss3.GetObjectOutput, error)
	DeleteObject(
		context.Context,
		*awss3.DeleteObjectInput,
		...func(*awss3.Options),
	) (*awss3.DeleteObjectOutput, error)
	ListObjectsV2(
		context.Context,
		*awss3.ListObjectsV2Input,
		...func(*awss3.Options),
	) (*awss3.ListObjectsV2Output, error)
}

type presignAPI interface {
	PresignGetObject(
		context.Context,
		*awss3.GetObjectInput,
		...func(*awss3.PresignOptions),
	) (*v4.PresignedHTTPRequest, error)
}

type Connector struct {
	code          filesystem.Code
	visibility    filesystem.Visibility
	bucket        string
	prefix        string
	publicBaseURL *url.URL
	client        objectAPI
	presigner     presignAPI
}

func New(ctx context.Context, config Config) (*Connector, error) {
	if ctx == nil {
		return nil, errors.New("s3 context is nil")
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	loadOptions := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(config.Region),
	}
	if config.AccessKeyID != "" || config.SecretAccessKey != "" {
		if config.AccessKeyID == "" || config.SecretAccessKey == "" {
			return nil, errors.New(
				"s3 access key id and secret access key must be configured together",
			)
		}
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				config.AccessKeyID,
				config.SecretAccessKey,
				config.SessionToken,
			),
		))
	}
	sdkConfig, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load s3 SDK config: %w", err)
	}

	client := awss3.NewFromConfig(sdkConfig, func(options *awss3.Options) {
		options.UsePathStyle = config.UsePathStyle
		if config.Endpoint != "" {
			options.BaseEndpoint = aws.String(config.Endpoint)
		}
	})

	var publicBaseURL *url.URL
	if config.PublicBaseURL != "" {
		publicBaseURL, err = url.Parse(config.PublicBaseURL)
		if err != nil || !publicBaseURL.IsAbs() || publicBaseURL.Host == "" {
			return nil, fmt.Errorf("invalid s3 public base URL %q", config.PublicBaseURL)
		}
	}

	return newConnector(
		config,
		client,
		awss3.NewPresignClient(client),
		publicBaseURL,
	), nil
}

func newConnector(
	config Config,
	client objectAPI,
	presigner presignAPI,
	publicBaseURL *url.URL,
) *Connector {
	return &Connector{
		code:          config.Code,
		visibility:    config.Visibility,
		bucket:        config.Bucket,
		prefix:        strings.Trim(config.Prefix, "/"),
		publicBaseURL: publicBaseURL,
		client:        client,
		presigner:     presigner,
	}
}

func (c *Connector) Code() filesystem.Code {
	return c.code
}

func (c *Connector) Visibility() filesystem.Visibility {
	return c.visibility
}

func (*Connector) KeyDistribution() filesystem.KeyDistribution {
	return filesystem.KeyDistributionSelfManaged
}

func (c *Connector) Ping(ctx context.Context) error {
	if ctx == nil {
		return errors.New("s3 ping context is nil")
	}
	_, err := c.client.HeadBucket(ctx, &awss3.HeadBucketInput{
		Bucket: aws.String(c.bucket),
	})
	if err != nil {
		return fmt.Errorf("head s3 bucket %q: %w", c.bucket, err)
	}
	return nil
}

func (c *Connector) PutNew(
	ctx context.Context,
	key string,
	source io.Reader,
	contentType string,
) error {
	return c.put(ctx, key, source, contentType, true)
}

func (c *Connector) Put(
	ctx context.Context,
	key string,
	source io.Reader,
	contentType string,
) error {
	return c.put(ctx, key, source, contentType, false)
}

func (c *Connector) put(
	ctx context.Context,
	key string,
	source io.Reader,
	contentType string,
	putNew bool,
) error {
	if ctx == nil {
		return errors.New("s3 put context is nil")
	}
	if source == nil {
		return errors.New("s3 source is nil")
	}
	key, err := c.objectKey(key)
	if err != nil {
		return err
	}
	body, cleanup, err := seekableBody(ctx, source)
	if err != nil {
		return fmt.Errorf("prepare s3 object %q body: %w", key, err)
	}
	defer cleanup()
	input := &awss3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if putNew {
		input.IfNoneMatch = aws.String("*")
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	if _, err := c.client.PutObject(ctx, input); err != nil {
		if putNew &&
			apiErrorCode(err, "PreconditionFailed", "ConditionalRequestConflict") {
			return filesystem.ErrConflict
		}
		return fmt.Errorf("put s3 object %q: %w", key, err)
	}
	return nil
}

func seekableBody(
	ctx context.Context,
	source io.Reader,
) (io.Reader, func(), error) {
	if seeker, ok := source.(io.ReadSeeker); ok {
		return seeker, func() {}, nil
	}

	temporary, err := os.CreateTemp("", "go-cms-s3-upload-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temporary upload file: %w", err)
	}
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}
	if _, err := io.Copy(temporary, contextReader{ctx: ctx, source: source}); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("spool upload stream: %w", err)
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("rewind upload stream: %w", err)
	}
	return temporary, cleanup, nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextReader) Read(target []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(target)
}

func (c *Connector) Open(
	ctx context.Context,
	key string,
) (io.ReadCloser, error) {
	if ctx == nil {
		return nil, errors.New("s3 open context is nil")
	}
	key, err := c.objectKey(key)
	if err != nil {
		return nil, err
	}
	result, err := c.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if apiErrorCode(err, "NoSuchKey", "NotFound") {
			return nil, filesystem.ErrNotFound
		}
		return nil, fmt.Errorf("get s3 object %q: %w", key, err)
	}
	return result.Body, nil
}

func (c *Connector) Delete(ctx context.Context, key string) error {
	if ctx == nil {
		return errors.New("s3 delete context is nil")
	}
	key, err := c.objectKey(key)
	if err != nil {
		return err
	}
	if _, err := c.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("delete s3 object %q: %w", key, err)
	}
	return nil
}

func (c *Connector) WalkPrefix(
	ctx context.Context,
	prefix string,
	visit func(string) error,
) error {
	if visit == nil {
		return errors.New("s3 prefix walker visitor is nil")
	}
	scan, err := c.OpenPrefixScan(ctx, prefix)
	if err != nil {
		return err
	}
	defer scan.Close()
	for {
		page, err := scan.Next(ctx, 1000)
		if err != nil {
			return err
		}
		for _, key := range page.Keys {
			if err := visit(key); err != nil {
				return err
			}
		}
		if page.Done {
			return nil
		}
	}
}

func (c *Connector) OpenPrefixScan(
	ctx context.Context,
	prefix string,
) (filesystem.PrefixScan, error) {
	if ctx == nil {
		return nil, errors.New("s3 prefix-scan context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalizedPrefix := strings.Trim(prefix, "/")
	if normalizedPrefix == "" {
		return nil, errors.New("s3 prefix-scan prefix is empty")
	}
	objectPrefix, err := c.objectKey(normalizedPrefix)
	if err != nil {
		return nil, err
	}
	stripPrefix := ""
	if c.prefix != "" {
		stripPrefix = c.prefix + "/"
	}
	return &prefixScan{
		client:       c.client,
		bucket:       c.bucket,
		objectPrefix: objectPrefix + "/",
		stripPrefix:  stripPrefix,
	}, nil
}

type prefixScan struct {
	mu           sync.Mutex
	client       objectAPI
	bucket       string
	objectPrefix string
	stripPrefix  string
	continuation *string
	done         bool
}

func (s *prefixScan) Next(
	ctx context.Context,
	limit int,
) (filesystem.PrefixScanPage, error) {
	if ctx == nil {
		return filesystem.PrefixScanPage{}, errors.New("s3 prefix-scan next context is nil")
	}
	if limit < 1 {
		return filesystem.PrefixScanPage{}, errors.New("s3 prefix-scan limit is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return filesystem.PrefixScanPage{Done: true}, nil
	}
	if limit > 1000 {
		limit = 1000
	}
	result, err := s.client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket:            aws.String(s.bucket),
		Prefix:            aws.String(s.objectPrefix),
		ContinuationToken: s.continuation,
		MaxKeys:           aws.Int32(int32(limit)),
	})
	if err != nil {
		return filesystem.PrefixScanPage{}, fmt.Errorf("scan s3 prefix %q: %w", s.objectPrefix, err)
	}
	page := filesystem.PrefixScanPage{Keys: make([]string, 0, len(result.Contents))}
	for _, object := range result.Contents {
		key := aws.ToString(object.Key)
		if s.stripPrefix != "" {
			var exists bool
			key, exists = strings.CutPrefix(key, s.stripPrefix)
			if !exists {
				return filesystem.PrefixScanPage{}, fmt.Errorf("s3 scan returned key outside connector prefix: %q", key)
			}
		}
		page.Keys = append(page.Keys, key)
	}
	if !aws.ToBool(result.IsTruncated) {
		s.done = true
		page.Done = true
		return page, nil
	}
	if aws.ToString(result.NextContinuationToken) == "" {
		return filesystem.PrefixScanPage{}, errors.New("s3 prefix scan omitted continuation token")
	}
	s.continuation = result.NextContinuationToken
	return page, nil
}

func (s *prefixScan) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	s.continuation = nil
	return nil
}

func (c *Connector) URL(
	_ context.Context,
	reference filesystem.Reference,
) (string, error) {
	if c.visibility != filesystem.VisibilityPublic {
		return "", filesystem.ErrInvalidVisibility
	}
	if c.publicBaseURL == nil {
		return "", errors.New("s3 public base URL is not configured")
	}
	key, err := c.objectKey(reference.Path)
	if err != nil {
		return "", err
	}
	result := *c.publicBaseURL
	// Assign the decoded path and let net/url escape each unsafe byte once.
	// The object key slash hierarchy remains intact.
	result.Path = strings.TrimRight(result.Path, "/") + "/" + key
	return result.String(), nil
}

func (c *Connector) TemporaryURL(
	ctx context.Context,
	reference filesystem.Reference,
	expiresAt time.Time,
) (string, error) {
	if c.visibility != filesystem.VisibilityPrivate {
		return "", filesystem.ErrInvalidVisibility
	}
	if ctx == nil {
		return "", errors.New("s3 temporary URL context is nil")
	}
	lifetime := time.Until(expiresAt)
	if lifetime <= 0 {
		return "", errors.New("temporary URL expiration must be in the future")
	}
	key, err := c.objectKey(reference.Path)
	if err != nil {
		return "", err
	}
	result, err := c.presigner.PresignGetObject(
		ctx,
		&awss3.GetObjectInput{
			Bucket: aws.String(c.bucket),
			Key:    aws.String(key),
		},
		func(options *awss3.PresignOptions) {
			options.Expires = lifetime
		},
	)
	if err != nil {
		return "", fmt.Errorf("presign s3 object %q: %w", key, err)
	}
	return result.URL, nil
}

func (c *Connector) Close() error {
	return nil
}

func (c *Connector) objectKey(key string) (string, error) {
	key = strings.Trim(key, "/")
	if key == "" || strings.ContainsRune(key, '\x00') {
		return "", errors.New("s3 object key is invalid")
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("s3 object key is invalid")
		}
	}
	if c.prefix == "" {
		return key, nil
	}
	return path.Join(c.prefix, key), nil
}

func validateConfig(config Config) error {
	switch {
	case config.Code == "":
		return errors.New("s3 code is empty")
	case !filesystem.ValidVisibility(config.Visibility):
		return fmt.Errorf(
			"s3 %q: %w: %q",
			config.Code,
			filesystem.ErrInvalidVisibility,
			config.Visibility,
		)
	case strings.TrimSpace(config.Region) == "":
		return errors.New("s3 region is empty")
	case strings.TrimSpace(config.Bucket) == "":
		return errors.New("s3 bucket is empty")
	case config.Visibility == filesystem.VisibilityPublic &&
		strings.TrimSpace(config.PublicBaseURL) == "":
		return errors.New("public s3 base URL is empty")
	default:
		return nil
	}
}

func apiErrorCode(err error, codes ...string) bool {
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	for _, code := range codes {
		if apiError.ErrorCode() == code {
			return true
		}
	}
	return false
}

var _ filesystem.Disk = (*Connector)(nil)
var _ filesystem.OverwriteDisk = (*Connector)(nil)
var _ filesystem.PrefixWalker = (*Connector)(nil)
var _ filesystem.PrefixScannerProvider = (*Connector)(nil)
var _ filesystem.KeyDistributionProvider = (*Connector)(nil)
var _ filesystem.Factory = Factory{}
