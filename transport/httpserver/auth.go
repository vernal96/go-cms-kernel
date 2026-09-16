package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	coreuser "github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

const (
	maximumAuthorizationBytes = 8 * 1024
	maximumLoginBodyBytes     = 4 * 1024
)

type credentialAuthenticator interface {
	Authenticate(
		context.Context,
		coreuser.AuthenticateInput,
	) (coreuser.User, error)
}

type loginHandler struct {
	authenticator credentialAuthenticator
	tokens        security.AccessTokenIssuer
}

type loginRequest struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

type loginResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresAt   string `json:"expires_at"`
}

func newLoginHandler(
	authenticator credentialAuthenticator,
	tokens security.AccessTokenIssuer,
) (*loginHandler, error) {
	if isNilHTTPValue(authenticator) {
		return nil, errors.New("login authenticator is nil")
	}
	if isNilHTTPValue(tokens) {
		return nil, errors.New("login access token issuer is nil")
	}
	return &loginHandler{
		authenticator: authenticator,
		tokens:        tokens,
	}, nil
}

func (h *loginHandler) ServeHTTP(
	response http.ResponseWriter,
	request *http.Request,
) {
	request.Body = http.MaxBytesReader(
		response,
		request.Body,
		maximumLoginBodyBytes,
	)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()

	var input loginRequest
	if err := decoder.Decode(&input); err != nil {
		writeLoginDecodeError(response, err)
		return
	}
	if err := ensureJSONEnd(decoder); err != nil {
		writeLoginDecodeError(response, err)
		return
	}
	if strings.TrimSpace(input.Identifier) == "" || input.Password == "" {
		httptransport.WriteJSONError(
			response,
			http.StatusBadRequest,
			"invalid_request",
			"identifier and password are required",
		)
		return
	}

	user, err := h.authenticator.Authenticate(
		request.Context(),
		coreuser.AuthenticateInput{
			Identifier: input.Identifier,
			Password:   input.Password,
		},
	)
	if err != nil {
		if errors.Is(err, coreuser.ErrInvalidCredentials) {
			writeUnauthorized(response, "invalid credentials")
			return
		}
		httptransport.WriteJSONError(
			response,
			http.StatusInternalServerError,
			"internal_error",
			"authentication failed",
		)
		return
	}
	if user.ID <= 0 {
		httptransport.WriteJSONError(
			response,
			http.StatusInternalServerError,
			"internal_error",
			"authentication failed",
		)
		return
	}

	accessToken, err := h.tokens.IssueAccessToken(
		request.Context(),
		security.User(user.ID),
	)
	if err != nil {
		httptransport.WriteJSONError(
			response,
			http.StatusInternalServerError,
			"internal_error",
			"access token issuance failed",
		)
		return
	}

	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(response).Encode(loginResponse{
		AccessToken: accessToken.Value,
		TokenType:   "Bearer",
		ExpiresAt:   accessToken.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func optionalAuthentication(
	verifier security.AccessTokenVerifier,
) httptransport.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(
			response http.ResponseWriter,
			request *http.Request,
		) {
			values := request.Header.Values("Authorization")
			if len(values) == 0 {
				next.ServeHTTP(
					response,
					request.WithContext(
						httptransport.WithActor(
							request.Context(),
							security.Guest(),
						),
					),
				)
				return
			}
			if len(values) != 1 ||
				len(values[0]) > maximumAuthorizationBytes {
				writeUnauthorized(response, "invalid access token")
				return
			}

			scheme, value, exists := strings.Cut(values[0], " ")
			if !exists ||
				!strings.EqualFold(scheme, "Bearer") ||
				value == "" ||
				strings.ContainsAny(value, " \t\r\n") {
				writeUnauthorized(response, "invalid access token")
				return
			}

			actor, err := verifier.VerifyAccessToken(
				request.Context(),
				value,
			)
			if err != nil {
				if errors.Is(err, security.ErrInvalidAccessToken) ||
					errors.Is(err, security.ErrUnauthenticated) {
					writeUnauthorized(response, "invalid access token")
					return
				}
				httptransport.WriteJSONError(
					response,
					http.StatusInternalServerError,
					"internal_error",
					"authentication failed",
				)
				return
			}
			if !actor.IsUser() {
				writeUnauthorized(response, "invalid access token")
				return
			}

			next.ServeHTTP(
				response,
				request.WithContext(
					httptransport.WithActor(request.Context(), actor),
				),
			)
		})
	}
}

func writeLoginDecodeError(response http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		httptransport.WriteJSONError(
			response,
			http.StatusRequestEntityTooLarge,
			"request_too_large",
			"request body is too large",
		)
		return
	}
	httptransport.WriteJSONError(
		response,
		http.StatusBadRequest,
		"invalid_request",
		"invalid JSON request",
	)
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("request contains multiple JSON values")
	}
	return err
}

func writeUnauthorized(response http.ResponseWriter, message string) {
	response.Header().Set("WWW-Authenticate", "Bearer")
	httptransport.WriteJSONError(
		response,
		http.StatusUnauthorized,
		"unauthenticated",
		message,
	)
}

var _ http.Handler = (*loginHandler)(nil)
