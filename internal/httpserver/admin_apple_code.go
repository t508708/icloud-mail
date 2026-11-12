package httpserver

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"icloud-api/internal/apple"
	"icloud-api/internal/hmesync"
)

func (s *Server) logAppleVerification(accountID int64, result hmesync.AuthResult, requestID string) {
	s.logAppleVerificationChannel(accountID, result, requestID, "icloud_web")
}

func (s *Server) logAppleVerificationChannel(accountID int64, result hmesync.AuthResult, requestID, channel string) {
	if v := result.Verification; v != nil {
		s.logger.Info("Apple 登录验证方式", "channel", channel,
			"account_id", accountID, "verification_method", v.Method, "verification_sent", v.Sent,
			"trusted_phone_count", len(v.Phones), "discovery_status", v.DiscoveryStatus,
			"delivery_status", v.DeliveryStatus, "request_id", requestID)
		s.logger.Info("Apple 验证选项读取结果", "channel", channel, "account_id", accountID, "options_source", v.OptionsSource,
			"phone_list_present", v.PhoneListPresent, "raw_phone_count", v.RawPhoneCount, "invalid_phone_count", v.InvalidPhoneCount,
			"sms_hidden", v.SMSHidden, "automatic_sms", v.AutomaticSMS, "delivery_unconfirmed", v.DeliveryWarning != "", "request_id", requestID)
	}
}

func (s *Server) adminAPIRequestAppleCode(c *gin.Context) {
	s.adminAPIRequestChannelCode(c, false)
}

func (s *Server) adminAPIRequestAccountCode(c *gin.Context) { s.adminAPIRequestChannelCode(c, true) }

func (s *Server) adminAPIRequestChannelCode(c *gin.Context, accountChannel bool) {
	id, ok := adminAPIParseID(c)
	if !ok || !s.adminAPIAppleAccountExists(c, id) {
		return
	}
	var input struct {
		ChallengeID string `json:"challenge_id"`
		Method      string `json:"method"`
		PhoneID     *int64 `json:"phone_id"`
	}
	if !decodeAdminAPIJSON(c, &input) {
		return
	}
	input.ChallengeID = strings.TrimSpace(input.ChallengeID)
	if input.ChallengeID == "" || len(input.ChallengeID) > 256 || (input.Method != "sms" && input.Method != "device") || input.Method == "sms" && input.PhoneID == nil || input.PhoneID != nil && (*input.PhoneID < 0 || *input.PhoneID > 9007199254740991) {
		writeAdminAPIError(c, http.StatusBadRequest, "VALIDATION_FAILED", "请选择有效的验证码接收方式")
		return
	}
	session := mustSession(c)
	var send func(context.Context, int64, int64, string, string, int64) (hmesync.AuthResult, error)
	action, channel := "apple_auth_code", "icloud_web"
	if accountChannel {
		if service, ok := s.hmeSync.(interface {
			RequestAccountAuthCode(context.Context, int64, int64, string, string, int64) (hmesync.AuthResult, error)
		}); ok {
			send = service.RequestAccountAuthCode
		}
		action, channel = "apple_account_auth_code", "apple_account"
	} else if service, ok := s.hmeSync.(interface {
		RequestAuthCode(context.Context, int64, int64, string, string, int64) (hmesync.AuthResult, error)
	}); ok {
		send = service.RequestAuthCode
	}
	if send == nil {
		s.adminAPIFinishAppleFailure(c, session, id, "apple_auth_code", adminAPIAppleServiceUnavailable())
		return
	}
	phoneID := int64(0)
	if input.PhoneID != nil {
		phoneID = *input.PhoneID
	}
	result, err := send(c.Request.Context(), session.AdminID, id, input.ChallengeID, input.Method, phoneID)
	s.logAppleVerificationChannel(id, result, requestID(c), channel)
	if err != nil {
		if delay := apple.RetryDelay(err); delay > 0 {
			seconds := int64(delay / time.Second)
			if delay%time.Second != 0 {
				seconds++
			}
			c.Header("Retry-After", strconv.FormatInt(seconds, 10))
		}
		s.adminAPIFinishAppleFailure(c, session, id, action, classifyAdminAPIAppleError(err))
		return
	}
	s.audit(c, &session.AdminID, session.Username, action, "account", strconv.FormatInt(id, 10), "success", input.Method)
	writeAdminAPIData(c, http.StatusAccepted, adminAPIAppleAuthResult(result))
}
