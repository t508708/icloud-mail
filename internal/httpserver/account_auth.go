package httpserver

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"icloud-api/internal/apple"
	"icloud-api/internal/hmesync"
)

type appleAccountAuthService interface {
	StartAccountAuth(context.Context, int64, int64, string, string, apple.Region) (hmesync.AuthResult, error)
	VerifyAccountAuth(context.Context, int64, int64, string, string) (hmesync.AuthResult, error)
	GetAccountSession(context.Context, int64) (hmesync.SessionInfo, error)
	ClearAccountAuth(context.Context, int64) error
}

func (s *Server) adminAPIAccountAuth(c *gin.Context) {
	id, ok := adminAPIParseID(c)
	if !ok || !s.adminAPIAppleAccountExists(c, id) {
		return
	}
	service, ok := s.hmeSync.(appleAccountAuthService)
	if !ok {
		writeAdminAPIError(c, 503, "APPLE_SERVICE_UNAVAILABLE", "Apple Account 服务暂未就绪")
		return
	}
	c.Header("Cache-Control", "no-store")
	admin := mustSession(c)
	var result hmesync.AuthResult
	var err error
	action := "apple_account_auth"
	switch c.Request.Method {
	case http.MethodGet:
		info, readErr := service.GetAccountSession(c.Request.Context(), id)
		if readErr != nil {
			s.adminAPIFinishAppleFailure(c, admin, id, action, classifyAccountAuthError(readErr))
			return
		}
		writeAdminAPIData(c, 200, gin.H{"apple_session": adminAPIAppleSessionFromInfo(info)})
		return
	case http.MethodDelete:
		err = service.ClearAccountAuth(c.Request.Context(), id)
		action = "apple_account_logout"
	default:
		if strings.HasSuffix(c.Request.URL.Path, "/verify") {
			var in adminAPIAppleVerifyRequest
			if !decodeAdminAPIJSON(c, &in) {
				return
			}
			if in.ChallengeID == "" || len(in.ChallengeID) > 256 || !adminAPISixDigitCode(in.Code) {
				writeAdminAPIError(c, 400, "VALIDATION_FAILED", "请填写六位验证码")
				return
			}
			result, err = service.VerifyAccountAuth(c.Request.Context(), admin.AdminID, id, in.ChallengeID, in.Code)
			in.Code = ""
			action = "apple_account_verify"
		} else {
			var in adminAPIAppleAuthRequest
			if !decodeAdminAPIJSON(c, &in) {
				return
			}
			in.AppleID = strings.TrimSpace(in.AppleID)
			if in.Region == "" {
				in.Region = "global"
			}
			if in.AppleID == "" || len(in.AppleID) > 320 || in.Password == "" || len(in.Password) > 1024 || in.Region != "global" && in.Region != "cn" {
				writeAdminAPIError(c, 400, "VALIDATION_FAILED", "请检查 Apple ID、密码与区域")
				return
			}
			result, err = service.StartAccountAuth(c.Request.Context(), admin.AdminID, id, in.AppleID, in.Password, apple.Region(in.Region))
			in.Password = ""
		}
	}
	if err != nil {
		s.adminAPIFinishAppleFailure(c, admin, id, action, classifyAccountAuthError(err))
		return
	}
	s.audit(c, &admin.AdminID, admin.Username, action, "account", strconv.FormatInt(id, 10), "success", result.Status)
	if c.Request.Method == http.MethodDelete {
		c.Status(http.StatusNoContent)
		return
	}
	status, valid := adminAPIAppleAuthHTTPStatus(result)
	if !valid {
		writeAdminAPIError(c, 502, "APPLE_UPSTREAM_ERROR", "Apple Account 登录结果待确认")
		return
	}
	writeAdminAPIData(c, status, adminAPIAppleAuthResult(result))
}

func classifyAccountAuthError(err error) adminAPIAppleError {
	switch hmesync.Code(err) {
	case hmesync.CodeAccountLoginRequired:
		return adminAPIAppleError{Status: 409, Code: hmesync.CodeAccountLoginRequired, Message: "请先登录 Apple Account 新通道"}
	case hmesync.CodeAccountSessionExpired:
		return adminAPIAppleError{Status: 409, Code: hmesync.CodeAccountSessionExpired, Message: "Apple Account 管理会话已过期，请重新登录新通道"}
	default:
		return classifyAdminAPIAppleError(err)
	}
}
