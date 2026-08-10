package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ObjectStore 基于 RustFS（S3 兼容对象存储）的多媒体缓存封装。
//
// 职责：
//   - Put：将媒体字节上传到桶，key = sha256(data)[:16] + 扩展名（内容寻址，天然去重）
//   - Get：按 key 下载对象内容
//   - Head：判断对象是否已存在（缓存命中判定）
//   - Presign：生成临时只读 URL，供视觉理解与发送图片使用
//
// 设计要点：
//   - 内容寻址 key 保证同一张图片只落一份对象，重复下载零成本；
//   - 桶不存在时 Put 自动创建并重试一次（启动不依赖网络可达）；
//   - 所有方法均返回可读错误，调用方（MediaPass）负责降级。
type ObjectStore struct {
	client    *s3.Client
	presigner *s3.PresignClient
	bucket    string
}

// NewObjectStore 创建 RustFS（S3 兼容）对象存储客户端。
// endpoint 形如 http://localhost:9000；accessKey/secretKey 为 S3 凭据；
// region 对 RustFS 无实际意义，传任意值（默认 us-east-1）。
func NewObjectStore(endpoint, accessKey, secretKey, bucket, region string) (*ObjectStore, error) {
	if endpoint == "" {
		return nil, errors.New("media: endpoint 为空")
	}
	if bucket == "" {
		return nil, errors.New("media: bucket 为空")
	}
	if region == "" {
		region = "us-east-1"
	}
	client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		UsePathStyle: true, // RustFS/MinIO 等兼容实现要求 path-style
	})
	return &ObjectStore{
		client:    client,
		presigner: s3.NewPresignClient(client),
		bucket:    bucket,
	}, nil
}

// Bucket 返回桶名。
func (s *ObjectStore) Bucket() string { return s.bucket }

// ensureBucket 创建桶；已存在或已拥有时忽略错误。
func (s *ObjectStore) ensureBucket(ctx context.Context) error {
	_, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(s.bucket),
	})
	if err == nil {
		return nil
	}
	// 桶已存在（自身或他人持有）视为成功
	if isBucketAlreadyExists(err) {
		return nil
	}
	return fmt.Errorf("media: 创建桶 %q 失败: %w", s.bucket, err)
}

// isBucketAlreadyExists 判断错误是否为"桶已存在/已拥有"。
// 兼容 AWS SDK v2 的强类型错误与 smithy.GenericAPIError 两种形式。
func isBucketAlreadyExists(err error) bool {
	var owned *s3types.BucketAlreadyOwnedByYou
	var exists *s3types.BucketAlreadyExists
	if errors.As(err, &owned) || errors.As(err, &exists) {
		return true
	}
	var aerr interface{ ErrorCode() string }
	if errors.As(err, &aerr) {
		switch aerr.ErrorCode() {
		case "BucketAlreadyExists", "BucketAlreadyOwnedByYou":
			return true
		}
	}
	return false
}

// isNoSuchBucket 判断错误是否为"桶不存在"。
// 兼容 AWS SDK v2 的强类型错误与 smithy.GenericAPIError 两种形式。
func isNoSuchBucket(err error) bool {
	var nsk *s3types.NoSuchBucket
	if errors.As(err, &nsk) {
		return true
	}
	var aerr interface{ ErrorCode() string }
	return errors.As(err, &aerr) && aerr.ErrorCode() == "NoSuchBucket"
}

// Put 上传媒体字节。mime 为空时按文件头嗅探。
// 返回对象 key（内容寻址：sha256(data)[:16] + 扩展名）。
// 若该 hash 对象已存在（缓存命中）则直接返回，不重复上传。
func (s *ObjectStore) Put(ctx context.Context, data []byte, mime string) (string, error) {
	if len(data) == 0 {
		return "", errors.New("media: 上传内容为空")
	}
	if mime == "" {
		mime = sniffMime(data)
	}
	hash := sha256.Sum256(data)
	key := hex.EncodeToString(hash[:8]) + extForMime(mime)

	// 内容寻址幂等：已存在直接返回
	if ok, err := s.Head(ctx, key); err != nil {
		return "", err
	} else if ok {
		return key, nil
	}

	input := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(mime),
	}
	if _, err := s.client.PutObject(ctx, input); err != nil {
		// 桶不存在 → 创建后重试一次
		if isNoSuchBucket(err) {
			if cerr := s.ensureBucket(ctx); cerr != nil {
				return "", fmt.Errorf("media: 上传 %s 失败且桶创建失败: %w", key, cerr)
			}
			if _, retryErr := s.client.PutObject(ctx, input); retryErr != nil {
				return "", fmt.Errorf("media: 上传 %s 失败: %w", key, retryErr)
			}
			return key, nil
		}
		return "", fmt.Errorf("media: 上传 %s 失败: %w", key, err)
	}
	return key, nil
}

// Get 下载对象内容。
func (s *ObjectStore) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("media: 下载 %s 失败: %w", key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, 64<<20)) // 单对象读取上限 64MB
	if err != nil {
		return nil, fmt.Errorf("media: 读取 %s 失败: %w", key, err)
	}
	return data, nil
}

// Head 判断对象是否存在（404 → false）。
func (s *ObjectStore) Head(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true, nil
	}
	var notFound *s3types.NotFound
	if errors.As(err, &notFound) {
		return false, nil
	}
	// 部分兼容实现返回 NoSuchKey API 错误
	var aerr interface{ ErrorCode() string }
	if errors.As(err, &aerr) && (aerr.ErrorCode() == "NoSuchKey" || aerr.ErrorCode() == "NoSuchBucket") {
		return false, nil
	}
	return false, fmt.Errorf("media: 检查 %s 失败: %w", key, err)
}

// Presign 生成临时只读下载 URL，用于视觉理解与发送图片。
func (s *ObjectStore) Presign(ctx context.Context, key string, ttl time.Duration) (string, error) {
	req, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("media: 生成 %s 预签名 URL 失败: %w", key, err)
	}
	return req.URL, nil
}
