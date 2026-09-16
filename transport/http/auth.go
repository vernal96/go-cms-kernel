package httptransport

import (
	"encoding/json"
	"net/http"
)

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func WriteJSONError(
	response http.ResponseWriter,
	status int,
	code string,
	message string,
) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(errorEnvelope{
		Error: errorBody{
			Code:    code,
			Message: message,
		},
	})
}

func RequireAuthenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		actor, exists := ActorFromContext(request.Context())
		if !exists {
			WriteJSONError(
				response,
				http.StatusInternalServerError,
				"internal_error",
				"request authentication is unavailable",
			)
			return
		}
		if !actor.IsUser() {
			response.Header().Set("WWW-Authenticate", "Bearer")
			WriteJSONError(
				response,
				http.StatusUnauthorized,
				"unauthenticated",
				"authentication required",
			)
			return
		}
		next.ServeHTTP(response, request)
	})
}
