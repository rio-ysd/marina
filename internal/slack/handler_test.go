package slack

import "testing"

func TestParseEventAuthorizations(t *testing.T) {
	// 本人のUser OAuthトークンに配信されたイベント。
	userPayload := []byte(`{"type":"event_callback","authorizations":[{"enterprise_id":null,"team_id":"T1","user_id":"U05RNEMF1CZ","is_bot":false,"is_enterprise_install":false}],"event":{"type":"message"}}`)
	auths := ParseEventAuthorizations(userPayload)
	if len(auths) != 1 {
		t.Fatalf("ParseEventAuthorizations() len = %d, want 1", len(auths))
	}
	if auths[0].UserID != "U05RNEMF1CZ" || auths[0].IsBot {
		t.Errorf("ParseEventAuthorizations() = %+v", auths[0])
	}
	if !authorizedForUser(auths, "U05RNEMF1CZ") {
		t.Error("authorizedForUser() = false, want true")
	}
	if authorizedForUser(auths, "U999") {
		t.Error("authorizedForUser() for other user = true, want false")
	}
}

func TestAuthorizedForUserRejectsBotAuthorization(t *testing.T) {
	// Botトークンに配信されたイベント(第三者からmarinaへのDM等)は本人の代理返信対象にしない。
	botPayload := []byte(`{"type":"event_callback","authorizations":[{"team_id":"T1","user_id":"U0BOT","is_bot":true}]}`)
	auths := ParseEventAuthorizations(botPayload)
	if authorizedForUser(auths, "U0BOT") {
		t.Error("authorizedForUser() for bot authorization = true, want false")
	}
}

func TestParseEventAuthorizationsInvalidPayload(t *testing.T) {
	if got := ParseEventAuthorizations([]byte("not json")); got != nil {
		t.Errorf("ParseEventAuthorizations(invalid) = %v, want nil", got)
	}
	if authorizedForUser(nil, "U05RNEMF1CZ") {
		t.Error("authorizedForUser(nil) = true, want false")
	}
}
