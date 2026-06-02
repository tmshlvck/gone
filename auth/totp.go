package auth

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// defaultTOTPIssuer is the issuer string embedded in generated
// otpauth URLs when AuthGORM.TOTPIssuer is empty. Authenticator apps
// display the issuer alongside the username so users can tell the
// app's accounts apart.
const defaultTOTPIssuer = "gone"

// totpGenerate produces a fresh shared secret + the otpauth URL +
// a PNG QR-code data URL ready to drop into <img src=...>. Used by
// the account-page enable-TOTP flow.
//
// The secret is the base32 form expected by every authenticator
// app; the otpauth URL is what scanning a QR pastes into the app.
func totpGenerate(issuer, username string) (secret, otpauthURL, qrDataURL string, err error) {
	if issuer == "" {
		issuer = defaultTOTPIssuer
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: username,
	})
	if err != nil {
		return "", "", "", fmt.Errorf("totp generate: %w", err)
	}
	img, err := key.Image(256, 256)
	if err != nil {
		return "", "", "", fmt.Errorf("totp qr image: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", "", "", fmt.Errorf("totp qr encode: %w", err)
	}
	qrDataURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	return key.Secret(), key.URL(), qrDataURL, nil
}

// totpValidate checks a 6-digit one-time code against the stored
// base32 secret. Constant-time via the underlying library. Accepts
// the ±1 step window (~30s on either side) that the RFC recommends.
func totpValidate(secret, code string) bool {
	return totp.Validate(code, secret)
}

// qrPNGDataURL renders the supplied otpauth URL as a 256x256 PNG QR
// code, base64-encoded into a data URL. Used by the verify-error
// rerender path that needs the QR back without minting a new secret.
func qrPNGDataURL(otpauthURL string) (string, error) {
	key, err := otp.NewKeyFromURL(otpauthURL)
	if err != nil {
		return "", fmt.Errorf("totp parse url: %w", err)
	}
	img, err := key.Image(256, 256)
	if err != nil {
		return "", fmt.Errorf("totp qr image: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", fmt.Errorf("totp qr encode: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
