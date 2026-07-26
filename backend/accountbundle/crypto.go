package accountbundle

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	envelopeVersion     = 1
	envelopeAlgorithm   = "aes-256-gcm"
	envelopeKDF         = "argon2id"
	defaultMemoryKiB    = 64 * 1024
	defaultIterations   = 3
	defaultParallelism  = 2
	credentialKeyBytes  = 32
	credentialSaltBytes = 16
)

func sealCredentials(password string, credentials map[string]CredentialSecret, aad []byte) (*CredentialEnvelope, error) {
	if strings.TrimSpace(password) == "" {
		return nil, errors.New("包含凭据时必须提供非空导出密码")
	}
	if len(aad) == 0 {
		return nil, errors.New("凭据加密缺少配置包认证数据")
	}
	payload, err := json.Marshal(credentials)
	if err != nil {
		return nil, fmt.Errorf("编码凭据失败: %w", err)
	}
	salt := make([]byte, credentialSaltBytes)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("生成凭据 salt 失败: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, defaultIterations, defaultMemoryKiB, defaultParallelism, credentialKeyBytes)
	aead, err := newCredentialAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("生成凭据 nonce 失败: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, payload, aad)
	return &CredentialEnvelope{
		Version:     envelopeVersion,
		Algorithm:   envelopeAlgorithm,
		KDF:         envelopeKDF,
		MemoryKiB:   defaultMemoryKiB,
		Iterations:  defaultIterations,
		Parallelism: defaultParallelism,
		Salt:        base64.StdEncoding.EncodeToString(salt),
		Nonce:       base64.StdEncoding.EncodeToString(nonce),
		Ciphertext:  base64.StdEncoding.EncodeToString(ciphertext),
	}, nil
}

func openCredentials(password string, envelope *CredentialEnvelope, aad []byte) (map[string]CredentialSecret, error) {
	if envelope == nil {
		return map[string]CredentialSecret{}, nil
	}
	if strings.TrimSpace(password) == "" {
		return nil, errors.New("该配置包包含受保护凭据，请提供导出密码")
	}
	if len(aad) == 0 {
		return nil, errors.New("凭据加密缺少配置包认证数据")
	}
	if envelope.Version != envelopeVersion || envelope.Algorithm != envelopeAlgorithm || envelope.KDF != envelopeKDF {
		return nil, errors.New("不支持的凭据加密信封")
	}
	if envelope.MemoryKiB < 8*1024 || envelope.MemoryKiB > 256*1024 || envelope.Iterations < 1 || envelope.Iterations > 10 || envelope.Parallelism < 1 || envelope.Parallelism > 16 {
		return nil, errors.New("凭据加密参数超出允许范围")
	}
	salt, err := base64.StdEncoding.DecodeString(envelope.Salt)
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return nil, errors.New("凭据 salt 无效")
	}
	nonce, err := base64.StdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, errors.New("凭据 nonce 无效")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, errors.New("凭据密文无效")
	}
	key := argon2.IDKey([]byte(password), salt, envelope.Iterations, envelope.MemoryKiB, envelope.Parallelism, credentialKeyBytes)
	aead, err := newCredentialAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != aead.NonceSize() {
		return nil, errors.New("凭据 nonce 长度无效")
	}
	payload, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, errors.New("导出密码错误或凭据数据已损坏")
	}
	var credentials map[string]CredentialSecret
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credentials); err != nil {
		return nil, errors.New("凭据内容无效")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, errors.New("凭据内容无效")
	}
	if credentials == nil {
		credentials = map[string]CredentialSecret{}
	}
	return credentials, nil
}

func newCredentialAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化凭据加密失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化凭据认证加密失败: %w", err)
	}
	return aead, nil
}

func credentialAAD(bundle *Bundle) ([]byte, error) {
	if bundle == nil {
		return nil, errors.New("凭据加密缺少配置包")
	}
	unsigned := *bundle
	unsigned.Credentials = nil
	encoded, err := json.Marshal(&unsigned)
	if err != nil {
		return nil, fmt.Errorf("编码凭据认证数据失败: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return []byte(fmt.Sprintf("%s:%d:credentials-v%d:%s", BundleSchema, BundleVersion, envelopeVersion, hex.EncodeToString(sum[:]))), nil
}
