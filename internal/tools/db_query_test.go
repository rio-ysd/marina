package tools

import "testing"

func TestValidateSelectQueryAllowsPlainSelect(t *testing.T) {
	got, err := validateSelectQuery("select * from tasks where id = 1;")
	if err != nil {
		t.Fatalf("validateSelectQuery() error: %v", err)
	}
	if got != "select * from tasks where id = 1" {
		t.Errorf("got %q", got)
	}
}

func TestValidateSelectQueryRejectsNonSelect(t *testing.T) {
	for _, q := range []string{
		"UPDATE tasks SET title = 'x'",
		"DELETE FROM tasks",
		"DROP TABLE tasks",
		"SELECT * FROM tasks; DROP TABLE tasks",
		"SELECT * INTO OUTFILE '/tmp/x' FROM tasks",
	} {
		if _, err := validateSelectQuery(q); err == nil {
			t.Errorf("validateSelectQuery(%q) expected error, got nil", q)
		}
	}
}

func TestValidateSelectQueryRejectsDeniedTable(t *testing.T) {
	if _, err := validateSelectQuery("SELECT access_token FROM oauth_tokens"); err == nil {
		t.Error("expected error for oauth_tokens table, got nil")
	}
}
