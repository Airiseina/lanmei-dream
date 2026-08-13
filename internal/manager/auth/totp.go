package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/DaWesen/lanmei-dream/internal/model"
)

// TOTP 参数（业界通用：SHA1 / 6 位 / 30 秒周期 / 前后各 1 周期容忍）
const (
	totpSecretSize = 20
	totpPeriod     = 30
	totpSkew       = 1
)

// totp 绑定流程的临时明文 secret（确认后才加密落库，明文不持久化）。
var totpPendingMu sync.Mutex
var totpPendingSecrets = map[uint]string{}

// BeginTOTPSetup 开始绑定 TOTP：生成密钥，返回 otpauth URL（前端生成二维码）。
// 明文 secret 仅本次返回；调用 ConfirmTOTPSetup 校验验证码后加密落库。
func (s *Service) BeginTOTPSetup(ctx context.Context, admin *model.ManagerAdmin) (string, string, error) {
	account := admin.Username
	issuer := s.cfg.WebAuthnDisplayName
	if issuer == "" {
		issuer = "Lanmei Manager"
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: account,
		SecretSize:  totpSecretSize,
		Period:      totpPeriod,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return "", "", fmt.Errorf("auth: totp generate: %w", err)
	}
	totpPendingMu.Lock()
	totpPendingSecrets[admin.ID] = key.Secret()
	totpPendingMu.Unlock()
	return key.Secret(), key.URL(), nil
}

// ConfirmTOTPSetup 校验 TOTP 验证码并加密保存凭据。
func (s *Service) ConfirmTOTPSetup(ctx context.Context, admin *model.ManagerAdmin, code string) error {
	totpPendingMu.Lock()
	secret, ok := totpPendingSecrets[admin.ID]
	totpPendingMu.Unlock()
	if !ok || secret == "" {
		return errors.New("auth: 请先发起 TOTP 绑定")
	}

	if !validateTOTP(secret, code) {
		return ErrTOTPInvalid
	}

	enc, err := s.box.Encrypt([]byte(secret))
	if err != nil {
		return err
	}
	totpPendingMu.Lock()
	delete(totpPendingSecrets, admin.ID)
	totpPendingMu.Unlock()

	cred, err := s.store.GetCredentialByKind(ctx, admin.ID, model.CredentialTOTP)
	if err != nil {
		return err
	}
	if cred == nil {
		cred = &model.AuthCredential{AdminID: admin.ID, Kind: model.CredentialTOTP, CredentialID: "totp"}
	}
	cred.Data = enc
	cred.Enabled = true
	if err := s.store.SaveCredentialUpsert(ctx, cred); err != nil {
		return fmt.Errorf("auth: save totp: %w", err)
	}
	return nil
}

// RemoveTOTP 解绑 TOTP（高危操作，调用方需已做 step-up）。
func (s *Service) RemoveTOTP(ctx context.Context, adminID uint) error {
	cred, err := s.store.GetCredentialByKind(ctx, adminID, model.CredentialTOTP)
	if err != nil {
		return err
	}
	if cred == nil {
		return nil
	}
	return s.store.DeleteCredential(ctx, cred.ID)
}

// verifyTOTPCredential 校验某管理员当前 TOTP 验证码。
func (s *Service) verifyTOTPCredential(ctx context.Context, adminID uint, code string) error {
	cred, err := s.store.GetCredentialByKind(ctx, adminID, model.CredentialTOTP)
	if err != nil {
		return err
	}
	if cred == nil || !cred.Enabled || len(cred.Data) == 0 {
		return ErrStepUpRequired
	}
	secret, err := s.box.Decrypt(cred.Data)
	if err != nil {
		return errors.New("auth: totp secret 解密失败")
	}
	if !validateTOTP(string(secret), code) {
		return ErrTOTPInvalid
	}
	return nil
}

// validateTOTP 校验验证码（含前后 1 周期容忍）。
func validateTOTP(secret, code string) bool {
	ok, err := totp.ValidateCustom(code, secret, time.Now(), totp.ValidateOpts{
		Period:    totpPeriod,
		Skew:      totpSkew,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	return err == nil && ok
}
