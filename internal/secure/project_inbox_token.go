package secure

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
)

const (
	inboxTokenContext      = "icloud-api/direct-link/token/inbox/v1"
	inboxTokenVersion      = byte(4)
	projectOTPTokenContext = "icloud-api/direct-link/token/project-otp/v1"
	projectOTPTokenPrefix  = "icp_"
	projectOTPTokenVersion = byte(1)
	projectOTPMACBytes     = 20
	maxProjectClientIDLen  = 128
)

var projectOTPTokenMagic = [3]byte{'p', 'o', 't'}

// InboxToken derives a read-only URL credential scoped strictly to viewing
// recent messages of one alias. Rotating the alias API key revokes it.
func (c *Cipher) InboxToken(aliasID int64, apiKeyHash []byte) (string, error) {
	return c.directLinkToken(aliasID, apiKeyHash, inboxTokenVersion, inboxTokenContext)
}

// InboxTokenAliasID parses an untrusted alias ID from an inbox share token.
func InboxTokenAliasID(token string) (int64, bool) {
	return directLinkTokenAliasID(token, inboxTokenVersion)
}

// VerifyInboxToken authenticates a read-only inbox share token against the
// alias's current API key hash.
func (c *Cipher) VerifyInboxToken(token string, aliasID int64, apiKeyHash []byte) bool {
	return c.verifyDirectLinkToken(token, aliasID, apiKeyHash, inboxTokenVersion, inboxTokenContext)
}

// ProjectOTPToken derives a project-bound URL credential for /api/v1/otp.
// Unlinking or disabling the project revokes this token without rotating the
// underlying alias credentials used by other projects.
func (c *Cipher) ProjectOTPToken(aliasID int64, apiKeyHash []byte, clientID string) (string, error) {
	if c == nil {
		return "", errors.New("项目取码令牌签名器不可用")
	}
	if aliasID < 1 {
		return "", errors.New("隐私邮箱 ID 必须是正整数")
	}
	if len(apiKeyHash) != sha256.Size {
		return "", errors.New("API Key 哈希必须是 32 字节")
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" || len(clientID) > maxProjectClientIDLen {
		return "", errors.New("项目标识长度无效")
	}

	headerLen := 3 + 1 + 8 + 1 + len(clientID)
	payload := make([]byte, headerLen+projectOTPMACBytes)
	copy(payload[:3], projectOTPTokenMagic[:])
	payload[3] = projectOTPTokenVersion
	binary.BigEndian.PutUint64(payload[4:12], uint64(aliasID))
	payload[12] = byte(len(clientID))
	copy(payload[13:headerLen], clientID)

	mac := hmac.New(sha256.New, c.directLinkKey[:])
	_, _ = mac.Write([]byte(projectOTPTokenContext))
	_, _ = mac.Write(payload[:headerLen])
	_, _ = mac.Write(apiKeyHash)
	copy(payload[headerLen:], mac.Sum(nil)[:projectOTPMACBytes])

	return projectOTPTokenPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

// ProjectOTPTokenIdentity extracts the untrusted alias ID and pool client ID
// from a project-scoped OTP token. Call VerifyProjectOTPToken before trusting.
func ProjectOTPTokenIdentity(token string) (aliasID int64, clientID string, ok bool) {
	payload, headerLen, valid := projectOTPTokenPayload(token)
	if !valid {
		return 0, "", false
	}
	encodedID := binary.BigEndian.Uint64(payload[4:12])
	id := int64(encodedID)
	if id < 1 || uint64(id) != encodedID {
		return 0, "", false
	}
	client := string(payload[13:headerLen])
	if strings.TrimSpace(client) != client || client == "" {
		return 0, "", false
	}
	return id, client, true
}

// VerifyProjectOTPToken authenticates a project-scoped OTP token against both
// the alias's current API key hash and the pool client ID.
func (c *Cipher) VerifyProjectOTPToken(token string, aliasID int64, apiKeyHash []byte, clientID string) bool {
	payload, headerLen, ok := projectOTPTokenPayload(token)
	if !ok || c == nil || len(apiKeyHash) != sha256.Size {
		return false
	}
	parsedAliasID, parsedClientID, ok := ProjectOTPTokenIdentity(token)
	if !ok || parsedAliasID != aliasID || parsedClientID != strings.TrimSpace(clientID) {
		return false
	}
	expected, err := c.ProjectOTPToken(aliasID, apiKeyHash, parsedClientID)
	if err != nil {
		return false
	}
	expectedPayload, expectedHeader, ok := projectOTPTokenPayload(expected)
	return ok && headerLen == expectedHeader &&
		hmac.Equal(payload[headerLen:], expectedPayload[expectedHeader:])
}

func projectOTPTokenPayload(token string) ([]byte, int, bool) {
	if !strings.HasPrefix(token, projectOTPTokenPrefix) || len(token) > 320 {
		return nil, 0, false
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(token[len(projectOTPTokenPrefix):])
	if err != nil || len(payload) < 13+1+projectOTPMACBytes {
		return nil, 0, false
	}
	if subtle.ConstantTimeCompare(payload[:3], projectOTPTokenMagic[:]) != 1 ||
		payload[3] != projectOTPTokenVersion {
		return nil, 0, false
	}
	clientLen := int(payload[12])
	if clientLen < 1 || clientLen > maxProjectClientIDLen {
		return nil, 0, false
	}
	headerLen := 13 + clientLen
	if len(payload) != headerLen+projectOTPMACBytes {
		return nil, 0, false
	}
	return payload, headerLen, true
}
